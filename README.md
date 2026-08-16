# Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Introduction

Go channels are simple not merely because they carry values. They connect
independent processes, synchronize them, and transfer responsibility for data.
After a successful send, a value logically disappears from the sender and
appears at the receiver. This property is what “quantum” refers to here.

NetChan extends the same model across processes and computers:

```go
connection.Send <- message
message = <-connection.Receive
```

In protocol v2, a network channel can carry values, reply channels, long-lived
session channels, and cancellation channels. It preserves message order,
provides backpressure, recovers from temporary disconnections, and distinguishes
receipt by the remote node from an actual read by the remote goroutine.

All limitations listed in the README for the first protocol version have been
addressed in v2. NetChan still does not hide the physics of a distributed system:
the network requires encoding and a temporary copy, while two independent Go
schedulers cannot perform a single atomic `select`. These boundaries are
represented by explicit channels and protocol states, rather than message loss
or hidden shared state.

> **Project status:** v2 is the current stable major version. New releases follow
> SemVer, pass mandatory security checks, and enter `main` only through pull
> requests.

## Why “Quantum” Network Channels

Within a single process, the Go runtime acts as a channel hypervisor: it knows
which goroutine is sending a value, which one is ready to read it, and when the
right to continue is transferred.

There is no shared runtime between computers. In NetChan v2, a logical session
and the acknowledgement protocol act as the network hypervisor:

1. the sender transfers a value to the local NetChan actor;
2. the value is encoded and registered on the remote side;
3. the remote application reads it from an ordinary Go channel;
4. the read acknowledgement returns to the sender;
5. the temporary encoded copy is released.

On the wire, this transition is represented by the sequence:

```text
DATA → PREPARED → DELIVERED → RELEASE
```

Here, “disappearance” means a transfer of logical ownership. After sending, the
sender no longer modifies the transferred maps, slices, or pointer-referenced
data; the receiver becomes the new owner after reading. The network needs a
physical copy for disconnection recovery, and it exists only until acknowledgement.

## What changed since the first version

| Protocol v1 limitation | Protocol v2 implementation |
|---|---|
| Sending was not synchronized with the remote read | `Deliver` completes only after the remote application reads the value |
| A network error could lose a message | Unacknowledged-message journal, ACKs, replay, and deduplication |
| Primarily values could be transferred | Messages can carry directional reply, session, and cancellation channels |
| Tasks could not be cancelled over the network | Closing a transferred `Done` propagates to the remote side |
| Buffering was implicit behavior | `Config.Capacity` and a bounded protocol window provide explicit backpressure |
| Gob was used | A custom bounded codec based on `encoding/binary` |
| A physical disconnection ended the exchange | The logical session persists and reconnects its transport |
| There was no strict ownership model | `DATA → PREPARED → DELIVERED → RELEASE` retains data until acknowledgement |
| A closed nested channel could leave state behind | A terminal barrier releases a capability after all parent messages complete |

## API overview

The library has two entry points:

```go
listener, err := netchan.Listen[Message](address)
connection, err := netchan.Dial[Message](address)
```

`Dial` returns one logical connection. `Listen` returns a listener, and every
connected client appears on its ordinary Go channel, `Channels`:

```go
connection := <-listener.Channels
```

The listener also exposes `Errors`, `Done`, and the actual bound `Address`. The
latter is especially useful with `Listen[Message]("127.0.0.1:0")`, when the
operating system selects the port.

Both sides receive the same `*netchan.Channel[T]` type:

```go
connection.Send       // chan<- T
connection.Receive    // <-chan T
connection.Errors     // <-chan error
connection.Done       // <-chan struct{}
```

`Send` and `Receive` are intentionally separate. Returning a single bidirectional
`chan T` would let a local sender rendezvous directly with a local receiver, so
the value might never reach the network. Two directional channels preserve
ordinary Go syntax and an unambiguous network boundary.

## Installation

```bash
go get github.com/matveynator/netchan/v2@latest
```

```go
import "github.com/matveynator/netchan/v2"
```

## Minimal server

The server accepts clients and starts an independent goroutine for each one.

```go
package main

import (
	"crypto/tls"
	"log"

	"github.com/matveynator/netchan/v2"
)

func serveConnection(connection *netchan.Channel[string]) {
	defer connection.Close()

	for message := range connection.Receive {
		connection.Send <- "Server received: " + message
	}
}

func main() {
	certificate, err := tls.LoadX509KeyPair("server.crt", "server.key")
	if err != nil {
		log.Fatal(err)
	}
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	}

	listener, err := netchan.Listen[string](
		"127.0.0.1:9876",
		netchan.Config{TLS: serverTLS},
	)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	for connection := range listener.Channels {
		go serveConnection(connection)
	}
}
```

## Minimal client

```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"

	"github.com/matveynator/netchan/v2"
)

func main() {
	certificatePEM, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatal(err)
	}
	rootCertificates := x509.NewCertPool()
	if !rootCertificates.AppendCertsFromPEM(certificatePEM) {
		log.Fatal("server CA certificate is invalid")
	}
	clientTLS := &tls.Config{
		RootCAs:    rootCertificates,
		ServerName: "netchan.example",
		MinVersion: tls.VersionTLS13,
	}

	connection, err := netchan.Dial[string](
		"127.0.0.1:9876",
		netchan.Config{TLS: clientTLS},
	)
	if err != nil {
		log.Fatal(err)
	}

	connection.Send <- "Hello"
	reply := <-connection.Receive
	fmt.Println(reply)

	close(connection.Send)
	<-connection.Done
}
```

This is the minimal form. In a long-lived application, sending and receiving
should be combined with `Done` through an ordinary `select`.

## `select`, `close`, and `range`

All public NetChan directions are real Go channels. They require no special read
or write methods.

```go
func exchange(connection *netchan.Channel[string], outgoing string) (string, bool) {
	select {
	case connection.Send <- outgoing:
	case <-connection.Done:
		return "", false
	}

	select {
	case incoming, open := <-connection.Receive:
		return incoming, open
	case <-connection.Done:
		return "", false
	}
}
```

For a graceful shutdown, the owner closes the sending side and waits for `Done`:

```go
func closeConnection(connection *netchan.Channel[string]) {
	close(connection.Send)
	<-connection.Done
}
```

The equivalent shorthand is:

```go
func closeConnectionWithMethod(connection *netchan.Channel[string]) error {
	return connection.Close()
}
```

After `Close`, the ordinary Go rules apply: the application must not send on a
closed channel. Stop sender goroutines first, then close the direction you own.

## Go channel properties implemented in v2

### Static typing

`Channel[T]`, `Listen[T]`, and `Dial[T]` preserve the concrete message type. The
compiler detects type errors, while a schema mismatch between peers is rejected
when the network channel opens.

### Transfer direction

The root `Send` and `Receive` channels, as well as nested `chan<- T` and
`<-chan T` channels, explicitly define who may send and receive. A nested
bidirectional `chan T` is not transferred because it does not define which right
should pass to the other side.

### Blocking and backpressure

An ordinary send blocks until the local NetChan actor accepts the value. If the
local buffer, network window, or bounded queues are full, the actor stops
accepting values and the `<-` operator naturally applies backpressure.

### Message ordering

Ordinary sends and `Deliver` share one sequence. Values accepted in order from
one goroutine are presented to the remote application in the same order. The
ordering of concurrent senders follows ordinary Go channel scheduling.

### Closing

`close(connection.Send)` declares that no more local values will be sent. Once
the logical channel finishes, `Receive`, `Errors`, and `Done` are closed, enabling
`range`, receives with `open`, and waiting in `select`.

### Buffering

By default, `Send` is unbuffered. Local capacity can be configured independently
on each side:

```go
func dialBuffered(address string) (*netchan.Channel[string], error) {
	return netchan.Dial[string](address, netchan.Config{Capacity: 32})
}
```

`Capacity` applies only to the local `Send`. `Receive` always remains unbuffered
so the protocol can determine the exact read point for `Deliver`.

### `select/case`

A goroutine can choose among the local directions of several network channels,
a timer, and a completion channel using the same `select` statement as with any
Go channels. A selected `case connection.Send <- message` means acceptance by
the local NetChan process; strict network rendezvous is expressed through
`Deliver`.

### A channel inside a channel

A directional channel in a message transfers the right to continue communicating,
not accumulated values. This is the primary mechanism for reply channels, session
channels, and remote cancellation without a client table or shared mutable state.

## Ordinary sending and strict rendezvous

A Go library cannot override the `<-` operator:

```go
connection.Send <- message
```

This operation completes when the local NetChan process accepts the value. At
that point, the sender has transferred ownership and must not modify the value.

Use `Deliver` when the sender must wait for the remote goroutine to actually read
the value:

```go
func sendAndWait(connection *netchan.Channel[string], message string) error {
	return connection.Deliver(message)
}
```

`Deliver` returns `nil` only after the remote `Receive` is read. If the channel
closes permanently, it returns `netchan.ErrChannelClosed`. An encoding error for
the specific value is also returned directly.

## A channel inside a channel: task, reply, and cancellation

Each task can create its own reply channel. The handler needs no client address,
request identifier, or shared table of pending results.

```go
type Result struct {
	Text string
}

type Task struct {
	Text  string
	Reply chan<- Result
	Done  <-chan struct{}
}
```

The client keeps its local channels and transfers only the required rights to the
handler:

```go
func requestTask(connection *netchan.Channel[Task], text string, cancel <-chan struct{}) (Result, bool) {
	reply := make(chan Result)
	done := make(chan struct{})
	defer close(done)

	task := Task{Text: text, Reply: reply, Done: done}
	select {
	case connection.Send <- task:
	case <-connection.Done:
		return Result{}, false
	}

	select {
	case result, open := <-reply:
		return result, open
	case <-cancel:
		return Result{}, false
	case <-connection.Done:
		return Result{}, false
	}
}
```

The handler replies directly to the task channel while observing its lifecycle:

```go
func handleTask(connection *netchan.Channel[Task], task Task) {
	defer close(task.Reply)

	select {
	case <-task.Done:
		return
	default:
	}

	result := Result{Text: "Done: " + task.Text}
	select {
	case task.Reply <- result:
	case <-task.Done:
	case <-connection.Done:
	}
}

func serveTasks(connection *netchan.Channel[Task]) {
	defer connection.Close()

	for task := range connection.Receive {
		go handleTask(connection, task)
	}
}
```

If the external `cancel` channel closes or the connection ends, the function
returns and closes the local `done` through `defer`. The closure crosses the
network, and the remote handler observes a closed `task.Done`. For long-running
work, the handler checks it between stages or builds an asynchronous streaming
pipeline.

A nested channel becomes active only after the application actually reads the
parent task. An unaccepted task does not start hidden work. One live channel may
be transferred in several messages: the remote side receives the same proxy.
After closure, a terminal barrier retains it until all previously accepted parent
messages are resolved, then releases the capability on both sides.

## Long-lived session channel

A reply channel usually lives until one response. A session channel remains in a
dedicated goroutine and carries a stream of messages until either side closes it.

```go
type Subscription struct {
	Events chan<- string
	Done   <-chan struct{}
}

func publishEvents(subscription Subscription, events <-chan string) {
	defer close(subscription.Events)

	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			select {
			case subscription.Events <- event:
			case <-subscription.Done:
				return
			}
		case <-subscription.Done:
			return
		}
	}
}
```

Such a process stores no subscriber state: the transferred channels hold the
entire right of contact.

## Reconnection and delivery guarantees

After a successful `Dial`, the application continues using the same `Channel`
even when the physical TCP/TLS transport is temporarily replaced. NetChan v2:

- replays unacknowledged frames after reconnection;
- never presents the same value to the application twice;
- distinguishes a transport ACK from an actual application read;
- preserves the order of ordinary sends and `Deliver` calls;
- retains a value until `RELEASE`;
- restores references to nested channels;
- closes the session if resume state is incompatible or lost.

Recovery attempts use increasing delays up to five seconds. A disconnected
logical session is retained for five minutes. If the peer has already lost its
state, the channel closes permanently and `Errors` may report
`netchan.ErrSessionExpired`. Refusal to accept a new session is returned from
`Dial` as `netchan.ErrSessionRejected`.

The no-duplicate-delivery guarantee applies within a live logical session. After
a complete restart of both processes, exactly-once delivery requires an
application transaction or a journal in persistent storage.

## Errors and aborting

`connection.Errors` and `listener.Errors` are best-effort diagnostic channels.
Closing the corresponding `Done` is the reliable terminal signal.

If an ordinary send accepts a value that cannot be encoded, NetChan reports the
error and terminates the channel: the value is not silently discarded. `Deliver`
returns such an error directly.

A graceful `Close` waits for values that have already been accepted. If delivery
is no longer required, use an explicit abort:

```go
func abortConnection(connection *netchan.Channel[string]) error {
	return connection.Abort()
}
```

`Abort` cancels queued transfers and does not promise to deliver them to the peer.

## Physical network boundaries

Protocol v2 no longer has the functional limitations listed for v1. The
properties that cannot be eliminated at the Go library level remain:

- two computers do not share a goroutine scheduler, so an atomic cross-machine
  `select` is impossible;
- wire transport requires encoding and a temporary physical copy;
- a process that loses its memory cannot recover exactly-once delivery without
  application-level persistent storage;
- finite memory requires bounded queues and input-size validation.

NetChan makes these boundaries explicit through `Deliver`, `Done`, `Errors`,
channel closure, backpressure, and logical session state.

## TLS

Every `Listen` and `Dial` requires `Config.TLS`. A call without TLS configuration
returns an error, preventing an application from accidentally enabling encryption
without peer authentication.

A production configuration uses an ordinary `*tls.Config`:

```go
func listenWithTLS(address string, serverTLS *tls.Config) (*netchan.Listener[string], error) {
	return netchan.Listen[string](address, netchan.Config{TLS: serverTLS})
}

func dialWithTLS(address string, clientTLS *tls.Config) (*netchan.Channel[string], error) {
	return netchan.Dial[string](address, netchan.Config{TLS: clientTLS})
}
```

This example requires the `crypto/tls` and
`github.com/matveynator/netchan/v2` imports. If `MinVersion` is unset, NetChan
selects TLS 1.3.

## Binary encoding

Protocol v2 uses a custom bounded format based on `encoding/binary`; it does not
use Gob.

Supported types include:

- booleans, numbers, and strings;
- structs with exported fields;
- arrays, slices, maps, and pointers;
- directional `chan<- T` and `<-chan T` channels inside messages;
- types implementing `MarshalBinary` and `UnmarshalBinary`.

Both sides of a root channel use the same concrete `T`. A nested channel's
direction is part of the schema. Interfaces, functions, `unsafe.Pointer`, cyclic
pointers, and nested bidirectional `chan T` channels are not transferred.

Generics are used only for the typed public boundary. `reflect` is confined to
the codec and is needed to build a schema for an arbitrary `T` and find
directional channels in a struct. A type can control its format completely
through `MarshalBinary` and `UnmarshalBinary`.

## Protective limits

The v2 limits are not missing capabilities. They prevent an untrusted peer or a
slow receiver from consuming memory without bound:

- wire frames are limited to 16 MiB;
- each value may contain up to 16 nested channels;
- a logical session may have up to 256 simultaneously live capabilities;
- up to 1,024 frames may remain unacknowledged;
- encoded, prepared, and decoded values have separate budgets;
- collection depth and size are bounded during decoding.

After the terminal barrier, a closed capability is released, so the number of
sequentially completed tasks is not limited by the number of simultaneously live
channels.

## Compatibility

The v2 Go module and generic API are intentionally incompatible with v1. Both
sides of a connection must use protocol v2 and matching message schemas.

| Version | Import path | Status |
|---|---|---|
| v1 | `github.com/matveynator/netchan` | Archived; no new fixes |
| v2 | `github.com/matveynator/netchan/v2` | Current supported version |

Versions are published as immutable Git tags following SemVer. A patch release
fixes bugs without changing the API, a minor release adds API compatibly, and an
incompatible change requires a new major version and import path. The latest
stable release is always available through the [permanent link](https://github.com/matveynator/netchan/releases/latest).

Protocol v2 is the second published version. Intermediate implementations
numbered 2, 3, and 4 during development were attempts at the same transition
from protocol v1 and are not considered separately released protocols.

## TODO and future transports

The current implementation uses TCP/TLS. Support for QUIC over UDP, Bluetooth
RFCOMM, BLE, automatic nearby discovery, and duplex QR exchange through a camera
and display has been deferred and is not yet part of the public API.

Agreed architectural decisions and milestones are recorded in [TODO.md](TODO.md).

## Project checks

```bash
go test ./...
go test -race ./...
go vet ./...

NETCHAN_NETWORK_TEST=1 go test -run 'TestPublic(ListenAndDial|ConfigAndListenerLifecycle|ClientsRemainIndependent|ListenerCloseTerminatesActiveChannel|ListenerCloseInterruptsIncompleteHandshake)$' -v .
```

## Community and support

Questions, suggestions, and bug reports are welcome in
[GitHub Issues](https://github.com/matveynator/netchan/issues). Real-world
scenarios involving temporary disconnections, long streaming sessions, and
channels inside messages are especially useful.

## Related projects

- [Netchan old version](https://github.com/matveynator/netchan-old) — an extension of Rob Pike's original idea;
- [Docker Libchan](https://github.com/docker/libchan) — a network message-passing interface;
- [GraftJS/jschan](https://github.com/graftjs/jschan) — a similar channel model for JavaScript;
- [Mat Ryer/Vice](https://github.com/matryer/vice) — Go channels in a distributed environment.

## License

NetChan is distributed under the [BSD 3-Clause License](LICENSE).
