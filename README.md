# “Quantum” Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Why NetChan

Go already has a convenient model for parallel work inside one computer: **split a large job into independent sequential tasks, run those tasks as goroutines, and connect them with channels**.

Start with an ordinary sequential computation:

```go
for i := 0; i < 1_000_000; i++ {
    check(i)
}
```

This is still one sequential task. Go will not automatically split this loop across CPU cores.

To use several cores, the application first decomposes the work into independent pieces and starts those pieces as separate goroutines. Channels are then used to coordinate them:

```go
func processRange(start, end int, done chan<- struct{}) {
    for i := start; i < end; i++ {
        check(i)
    }

    done <- struct{}{}
}

func processAll() {
    done := make(chan struct{})

    go processRange(0,       250_000, done)
    go processRange(250_000, 500_000, done)
    go processRange(500_000, 750_000, done)
    go processRange(750_000, 1_000_000, done)

    for i := 0; i < 4; i++ {
        <-done
    }
}
```

Now there are four independent sequential tasks. The Go runtime can schedule those goroutines concurrently and, when several CPU cores are available, execute them in parallel.

```text
Task A -> go processRange(...) -> CPU Core 1
Task B -> go processRange(...) -> CPU Core 2
Task C -> go processRange(...) -> CPU Core 3
Task D -> go processRange(...) -> CPU Core 4
```

The roles are simple:

```text
goroutines -> independently schedulable pieces of work
channels   -> communication and synchronization between them
select     -> wait for several possible channel events
Go runtime -> schedules runnable goroutines on available CPU cores
```

So a channel does not make one sequential loop parallel by itself. The application decomposes the job into tasks; goroutines execute those tasks; channels connect and coordinate them.

That works very well inside one Go runtime, but a native Go `chan` does not directly cross from one computer to another.

**NetChan extends this channel model across the network.**

The goroutine itself does not move to another machine. Instead, the same task/channel worker model can continue in another process, while NetChan carries values and directional channel capabilities across the network boundary.

The important change is where a task may be executed:

```text
ordinary Go

Task A -> Machine A / Core 1
Task B -> Machine A / Core 2
Task C -> Machine A / Core 3
Task D -> Machine A / Core 4


NetChan

Task A -> Machine A / Core 1
Task B -> Machine A / Core 2
Task C -> Machine B / Core 1
Task D -> Machine B / Core 2
Task E -> Machine C / Core 1
Task F -> Machine D / Core 8
```

In short:

```text
Go:
decompose a large job into independent sequential tasks,
run them as goroutines,
coordinate them with channels,
and execute them in parallel on cores of one computer

NetChan:
extend the same channel-oriented model across the network,
so application work can also be handled by processes on other computers
```

NetChan does not make one sequential operation itself faster. It lets the same decomposition-and-channel model scale beyond one machine while keeping the programming style close to ordinary Go: goroutines, channels, `select`, and `close`.

---

## What NetChan provides

NetChan itself does **not** define a `Task`, `ManagedTask`, scheduler, request/reply protocol, or a required number of nested channels.

The library provides a more general primitive:

```text
typed Go value
    +
zero or more directional channels inside that value
    +
network transport
```

The root message may be any supported Go type. If that type contains directional channels, NetChan transfers those channel capabilities with the value.

For example, an application is free to define something like:

```go
type Message struct {
    ID      int
    Payload Payload
    Control <-chan Command
    Result  chan<- Result
    Events  chan<- Event
}
```

Another application may use no nested channels, one channel, two channels, or several channels, subject only to the protocol's protective limits. Their field names, message types, meaning, and lifetime belong to the application.

A nested channel may represent a reply path, cancellation, progress, events, a subscription, a long-lived session, or something completely different. NetChan transports the typed value and the channel capability; it does not assign application semantics to them.

This separation is intentional. Distributed task execution is an important use case, but it is a pattern built **on top of** NetChan rather than a restriction imposed by the library.

---

## A common task-oriented pattern

One particularly useful application pattern is a distributed worker pool. Tasks are published into a shared channel, and free workers block waiting for the next one.

```text
Task A --\
Task B ---+--> shared task channel --> Worker 1
Task C ---+                         --> Worker 2
Task D --/                          --> Worker 3
```

Once a worker receives a task, it executes that task's ordinary sequential code and then returns to the channel for more work.

A worker may be another goroutine on the same machine or the same worker program running on another computer. From the task's point of view, only the execution location changes:

```text
Task A -> local worker  -> Machine A / Core 1
Task B -> local worker  -> Machine A / Core 2
Task C -> remote worker -> Machine B / Core 1
Task D -> remote worker -> Machine C / Core 3
```

This scheduler and worker-pool behavior belongs to the application. NetChan supplies the channel transport that makes the remote workers possible.

---

## A simple task lifecycle convention

For task-oriented applications, one convenient convention is to use either a one-way task or a task with a temporary request/reply pair.

This is a **recommended application pattern, not a NetChan protocol rule**.

```text
Fire-and-Forget
Managed Task
```

### Fire-and-Forget

If the sender does not need a result or further control, the application may send only the work:

```go
type Task struct {
    Work Work
}
```

```text
Task Owner ---- Work ----> Worker
```

The task is sent, executed, and forgotten. No task-local communication channels are needed.

### Managed Task

If the sender needs a result or wants to control the task while it is running, a useful convention is to include two task-scoped directional channels:

```go
type ManagedTask struct {
    Work    Work
    Request <-chan Request
    Reply   chan<- Reply
}
```

The names here are from the point of view of the **task owner**, the side that creates and dispatches the task:

```text
Task Owner                         Worker

          Work --------------------->
          Request ------------------->
          <---------------------- Reply
```

`Request` is owner -> worker. It may carry cancellation, parameter changes, clarification, progress requests, or other control messages.

`Reply` is worker -> owner. It may carry the final result, progress, status, partial results, or errors.

Each channel is simplex; together they form a temporary duplex session for that task.

A worker can combine ordinary sequential computation with control using normal Go `select`:

```go
for candidate := start; candidate < end; candidate++ {
    select {
    case request, open := <-task.Request:
        if !open {
            return
        }
        handleRequest(request)
    default:
    }

    check(candidate)
}
```

If cancellation is all that is needed in this convention, closing the request side is enough. A receive from a closed channel becomes immediately selectable, so the worker can stop and return to the pool.

For this pattern, keeping the shape simple is useful:

```text
Fire-and-Forget:
    Work

Managed Task:
    Work + Request + Reply
```

But NetChan does not enforce that shape. An application may define a different task type with one control channel, several result channels, event streams, separate cancellation and progress channels, or any other arrangement supported by the protocol.

When an application chooses the `Request` / `Reply` convention, the pair is naturally task-scoped: a task gets a fresh pair, that pair ends with the task, and the next task gets different channels.

```text
Task A
 ├── Request A
 └── Reply A

Task A ends

Task B
 ├── Request B
 └── Reply B
```

The same pair is not reused for another logical task.

---

## Competitive execution

A scheduler does not have to assign exactly one worker to every logical task.

If there are many idle workers, several of them may execute copies of the same important task:

```text
Task A -> Worker 01
Task A -> Worker 02
Task A -> Worker 03
...
```

If the application uses the `Request` / `Reply` convention, the first valid reply can win:

```text
Worker 01 -------\
Worker 02 --------\
Worker 03 ---------> first valid Reply wins
Worker 04 --------/
Worker 05 -------/
```

The task owner can then stop the remaining copies through the request/control path. The losing workers return to the common pool and can immediately take other work.

This is an application-level form of distributed speculative execution.

---

## Decomposition instead of competition

Competition is only one scheduling strategy. Often the better approach is to split a large search or computation into many smaller independent tasks:

```text
0 ------------------------------------------------------ 100,000,000
```

becomes:

```text
Task 1:   0 - 999,999
Task 2:   1,000,000 - 1,999,999
Task 3:   2,000,000 - 2,999,999
Task 4:   3,000,000 - 3,999,999
...
```

and workers consume them as they become free:

```text
Task 1 -> Machine A / Core 1
Task 2 -> Machine A / Core 2
Task 3 -> Machine B / Core 1
Task 4 -> Machine B / Core 2
Task 5 -> Machine C / Core 1
```

Each task remains ordinary sequential code. Fast workers naturally complete more tasks; slow workers complete fewer.

---

## Heterogeneous workers

A distributed pool does not need identical hardware. It may contain old laptops, desktop CPUs, large servers, ARM machines, cloud VMs, GPUs, storage-heavy nodes, or specialized accelerators.

Workers can keep application-level performance statistics such as:

```text
operations / second
iterations / second
average task runtime
factoring speed
encoding speed
rendering speed
```

A scheduler can use those measurements to decide where a task should run or how large a task should be:

```text
Task target: < 500 ms

Worker A estimate: 8.2 s   -> unsuitable
Worker B estimate: 190 ms  -> execute
```

NetChan provides the communication model; scheduling policy belongs to the application.

---

## Example workloads

The worker-pool pattern is useful whenever a larger problem can be decomposed into independent work, for example:

- factoring and mining-style workloads;
- large search spaces;
- rendering;
- video and media processing;
- compression;
- scientific simulations and Monte Carlo workloads;
- crawling and indexing;
- distributed builds;
- testing and fuzzing;
- batch data processing;
- independent AI inference jobs.

These are examples, not limits on what NetChan can transport. Applications may use nested network channels for many other communication patterns as well.

---

## The idea in one picture

```text
one sequential computation
        |
        v
decompose into independent tasks
        |
        +--> Task A
        +--> Task B
        +--> Task C
        +--> Task D
                |
                v
        Go goroutines + chan
                |
                v
      cores of one machine
                |
                v
             NetChan
                |
                v
   other processes and machines
                |
                v
     distributed worker pool
```

A common task convention is:

```text
Fire-and-Forget: Work
Managed Task:    Work + Request + Reply
```

But the NetChan primitive itself remains more general:

```text
supported typed value
    +
zero or more nested directional channels
```

---

## How NetChan works

Go channels are simple not merely because they carry values. They connect
independent processes, synchronize them, and transfer responsibility for data.
After a successful send, a value logically disappears from the sender and
appears at the receiver. This property is what “quantum” refers to here.

NetChan extends the same model across processes and computers:

```go
connection.Send <- message
message = <-connection.Receive
```

In protocol v2, a network channel can carry ordinary supported values and
directional channels inside those values. NetChan does not interpret the nested
channels as requests, replies, cancellation, or sessions; those meanings belong
to the application.

It preserves message order, provides backpressure, recovers from temporary
disconnections, and distinguishes receipt by the remote node from an actual read
by the remote goroutine.

A distributed task system is one common application. Such an application may use
no nested channels for one-way work, or a temporary `Request` / `Reply` pair for
control and results. Another application may choose a completely different set of
nested channels.

NetChan still does not hide the physics of a distributed system: the network
requires encoding and a temporary copy, while two independent Go schedulers
cannot perform a single atomic cross-machine `select`. These boundaries are
represented by explicit channels and protocol states rather than message loss or
hidden shared state.

> **Project status:** v2 is the current major version. New releases follow SemVer,
> pass mandatory security checks, and enter `main` only through pull requests.

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

On the wire, this transition is represented by:

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
| Primarily values could be transferred | Messages can carry directional nested channels |
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

`Message` is application-defined. It may be any supported concrete Go type, and
it may contain directional nested channels where the application needs them.

`Dial` returns one logical connection. `Listen` returns a listener, and every
connected client appears on its ordinary Go channel, `Channels`:

```go
connection := <-listener.Channels
```

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

    listener, err := netchan.Listen[string](
        "127.0.0.1:9876",
        netchan.Config{TLS: &tls.Config{
            Certificates: []tls.Certificate{certificate},
            MinVersion:   tls.VersionTLS13,
        }},
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

    roots := x509.NewCertPool()
    if !roots.AppendCertsFromPEM(certificatePEM) {
        log.Fatal("server CA certificate is invalid")
    }

    connection, err := netchan.Dial[string](
        "127.0.0.1:9876",
        netchan.Config{TLS: &tls.Config{
            RootCAs:    roots,
            ServerName: "netchan.example",
            MinVersion: tls.VersionTLS13,
        }},
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

## `select`, `close`, and `range`

All public NetChan directions are real Go channels.

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

For graceful shutdown, the owner closes the sending side and waits for `Done`:

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

After `Close`, ordinary Go rules apply: the application must not send on a closed
channel. Stop sender goroutines first, then close the direction you own.

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

By default, `Send` is unbuffered. Local capacity can be configured independently:

```go
func dialBuffered(address string) (*netchan.Channel[string], error) {
    return netchan.Dial[string](address, netchan.Config{Capacity: 32})
}
```

`Capacity` applies only to the local `Send`. `Receive` remains unbuffered so the
protocol can determine the exact application read point for `Deliver`.

### A channel inside a channel

A directional channel inside a message transfers the right to continue
communicating, not accumulated values.

NetChan assigns no application role to that nested channel. It may represent a
reply, control path, event stream, progress stream, subscription, session, or
another application-defined capability. Several directional nested channels may
coexist in the same value, subject to protocol limits.

## Ordinary sending and strict rendezvous

A Go library cannot override the `<-` operator:

```go
connection.Send <- message
```

This completes when the local NetChan actor accepts the value. At that point, the
sender has transferred ownership and must not modify the value.

Use `Deliver` when the sender must wait for the remote goroutine to actually read
the value:

```go
func sendAndWait(connection *netchan.Channel[string], message string) error {
    return connection.Deliver(message)
}
```

`Deliver` returns `nil` only after the remote `Receive` is read. If the channel
closes permanently, it returns `netchan.ErrChannelClosed`.

## Task-oriented example

The following `Request` / `Reply` structure is only one application pattern. It
is not a type or lifecycle imposed by NetChan:

```go
type TaskRequest struct {
    Stop bool
}

type TaskReply struct {
    Result string
}

type Task struct {
    Text    string
    Request <-chan TaskRequest
    Reply   chan<- TaskReply
}
```

The task owner creates a fresh pair for this task and transfers the worker-facing
directions inside the task.

Conceptually:

```text
create Task A
    |
    +--> create Request A
    +--> create Reply A
    |
    v
send Task A
    |
    v
worker executes Task A
    |
    +--> receive control through Request A
    +--> send information through Reply A
    |
    v
Task A ends
    |
    +--> Request A ends
    +--> Reply A ends
```

A worker can combine computation with task control using ordinary `select`:

```go
func handleTask(task Task) {
    for step := 0; ; step++ {
        select {
        case request, open := <-task.Request:
            if !open || request.Stop {
                return
            }
        default:
        }

        result, found := calculateStep(task.Text, step)
        if found {
            task.Reply <- TaskReply{Result: result}
            return
        }
    }
}
```

The exact structure is application-defined. Another application may use one
nested channel, several independent result channels, a separate cancellation
channel, or no task concept at all.

## Long-lived nested channels

Nested channels do not have to be task-scoped. An application may deliberately
transfer a longer-lived capability and keep it in a dedicated goroutine until its
own application lifecycle ends.

For example:

```go
type Subscription struct {
    Events chan<- string
    Done   <-chan struct{}
}
```

The lifetime and meaning of these channels are defined by the application, not by
NetChan.

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
`netchan.ErrSessionExpired`.

The no-duplicate-delivery guarantee applies within a live logical session. After
a complete restart of both processes, exactly-once delivery requires an
application transaction or a journal in persistent storage.

## Errors and aborting

`connection.Errors` and `listener.Errors` are best-effort diagnostic channels.
Closing the corresponding `Done` is the reliable terminal signal for the root
logical connection.

If an ordinary send accepts a value that cannot be encoded, NetChan reports the
error and terminates the channel. `Deliver` returns such an error directly.

A graceful `Close` waits for values already accepted. If delivery is no longer
required, use an explicit abort:

```go
func abortConnection(connection *netchan.Channel[string]) error {
    return connection.Abort()
}
```

`Abort` cancels queued transfers and does not promise to deliver them to the peer.

## Physical network boundaries

NetChan cannot eliminate the physical properties of a distributed system:

- two computers do not share a goroutine scheduler, so an atomic cross-machine
  `select` is impossible;
- wire transport requires encoding and a temporary physical copy;
- a process that loses its memory cannot recover exactly-once delivery without
  application-level persistent storage;
- finite memory requires bounded queues and input-size validation.

NetChan makes these boundaries explicit through `Deliver`, root-channel `Done`,
`Errors`, nested-channel closure, backpressure, and logical session state.

## TLS

Every `Listen` and `Dial` requires `Config.TLS`.

```go
func listenWithTLS(address string, serverTLS *tls.Config) (*netchan.Listener[string], error) {
    return netchan.Listen[string](address, netchan.Config{TLS: serverTLS})
}

func dialWithTLS(address string, clientTLS *tls.Config) (*netchan.Channel[string], error) {
    return netchan.Dial[string](address, netchan.Config{TLS: clientTLS})
}
```

If `MinVersion` is unset, NetChan selects TLS 1.3.

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
directional channels in a struct.

## Protective limits

The v2 limits prevent an untrusted peer or slow receiver from consuming memory
without bound:

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
incompatible change requires a new major version and import path.

## TODO and future transports

The current implementation uses TCP/TLS. Support for QUIC over UDP, Bluetooth
RFCOMM, BLE, automatic nearby discovery, duplex QR exchange through a camera and
display, and mesh-style routing or relaying of channel capabilities have been
deferred and are not yet part of the public API.

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
scenarios involving temporary disconnections, distributed worker pools, nested
channels with application-defined lifetimes, and channels inside messages are
especially useful.

## Related projects

- [Netchan old version](https://github.com/matveynator/netchan-old) — an extension of Rob Pike's original idea;
- [Docker Libchan](https://github.com/docker/libchan) — a network message-passing interface;
- [GraftJS/jschan](https://github.com/graftjs/jschan) — a similar channel model for JavaScript;
- [Mat Ryer/Vice](https://github.com/matryer/vice) — Go channels in a distributed environment.

## License

NetChan is distributed under the [BSD 3-Clause License](LICENSE).
