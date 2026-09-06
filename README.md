# “Quantum” Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Why NetChan

Go already has a very convenient model for parallel work inside one computer: **goroutines connected by channels**.

A goroutine can execute an ordinary sequential piece of code, while `chan` carries work and results between goroutines and synchronizes them. `select` lets a worker wait for work, cancellation, or another event using ordinary Go syntax.

This makes a common pattern simple:

```text
one large sequential problem
          |
          v
split into independent sequential tasks
          |
          +--> goroutine A -> CPU Core 1
          +--> goroutine B -> CPU Core 2
          +--> goroutine C -> CPU Core 3
          +--> goroutine D -> CPU Core 4
```

The individual tasks remain sequential. The speedup comes from executing several independent tasks at the same time on different CPU cores.

For example, a worker may still contain nothing more complicated than:

```go
for i := start; i < end; i++ {
    check(i)
}
```

The Go runtime schedules runnable goroutines and can execute independent ones in parallel on the cores available in that machine.

A native Go `chan`, however, belongs to one Go runtime. It does not directly connect a goroutine on one computer with a goroutine on another.

**NetChan extends this channel model across the network.**

Instead of being limited to the CPU cores of one machine, the same task-oriented design can use workers running on other computers and therefore their CPU cores as well:

```text
ordinary Go

Machine A / Core 1 -> Task A
Machine A / Core 2 -> Task B
Machine A / Core 3 -> Task C
Machine A / Core 4 -> Task D


NetChan

Machine A / Core 1 -> Task A
Machine A / Core 2 -> Task B
Machine B / Core 1 -> Task C
Machine B / Core 2 -> Task D
Machine C / Core 1 -> Task E
Machine D / Core 8 -> Task F
```

In short:

```text
Go channels:
parallelize independent sequential tasks
across cores of one computer

NetChan:
extend the same channel-oriented model
across multiple computers and their cores
```

NetChan does not make one sequential operation itself faster. It makes it possible to scale a decomposable workload beyond one machine while keeping the programming model close to ordinary Go: goroutines, channels, `select`, and `close`.

---

## Shared task channel

A simple distributed worker pool uses a shared task channel. Free workers block waiting for work; when a task arrives, one worker receives it, executes its ordinary sequential code, and then waits for the next task.

```text
                     +----------+
                  +->| Worker 1 |
                  |  +----------+
                  |
+-----------+     |  +----------+
| Scheduler |-----+->| Worker 2 |
+-----------+     |  +----------+
                  |
                  |  +----------+
                  +->| Worker 3 |
                     +----------+
```

A worker may be another goroutine on the same machine or the same worker program running on another computer. The application-level model stays the same.

---

## Two task modes

A task can be used in one of two ways:

```text
1. Fire-and-Forget
2. Managed Task
```

### 1. Fire-and-Forget

If the sender does not need a result or further control, the task contains only the work:

```go
type Task struct {
    Work Work
}
```

```text
Task Owner ---- Work ----> Worker
```

The task is sent, executed, and forgotten. No task-local communication channels are created.

### 2. Managed Task

If the sender needs a result or wants to control the task while it is running, the task carries a fresh pair of task-scoped directional channels:

```go
type ManagedTask struct {
    Work    Work
    Request <-chan Request
    Reply   chan<- Reply
}
```

Their names are always from the point of view of the **task owner**, the side that creates and dispatches the task:

```text
Task Owner                         Worker

          Work --------------------->
          Request ------------------->
          <---------------------- Reply
```

`Request` is owner -> worker. It can carry cancellation, parameter changes, clarification, progress requests, or other control messages.

`Reply` is worker -> owner. It can carry the final result, progress, status, partial results, or errors.

Each channel is simplex; together they form a temporary duplex session for exactly one task.

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

If cancellation is all that is needed, closing the request side is enough. A receive from a closed channel becomes immediately selectable, so the worker can stop and return to the pool.

The model is deliberately binary:

```text
Fire-and-Forget:
    Work

Managed Task:
    Work + Request + Reply
```

There is no managed form with only one of the two channels.

`Request` and `Reply` are also **task-scoped**. A managed task gets a new pair when it is created, and that pair ends with the task. The next task gets different channels:

```text
Task A
 ├── Request A
 └── Reply A

Task A ends

Task B
 ├── Request B
 └── Reply B
```

The same pair is never reused for another logical task.

---

## Competitive execution

A scheduler does not have to assign exactly one worker to every logical task.

If there are many idle workers, several of them may execute copies of the same important managed task:

```text
Task A -> Worker 01
Task A -> Worker 02
Task A -> Worker 03
...
```

```text
Worker 01 -------\
Worker 02 --------\
Worker 03 ---------> first valid Reply wins
Worker 04 --------/
Worker 05 -------/
```

When the first acceptable result arrives through `Reply`, the task owner can stop the remaining copies through `Request`. The losing workers return to the common pool and can immediately take other work.

This is a simple form of distributed speculative execution.

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
Machine A / Core 1 -> Task 1
Machine A / Core 2 -> Task 2
Machine B / Core 1 -> Task 3
Machine B / Core 2 -> Task 4
Machine C / Core 1 -> Task 5
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

The model is useful whenever a larger problem can be decomposed into independent work, for example:

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

Workers may represent CPUs, GPUs, memory-heavy machines, storage nodes, network resources, accelerators, or external hardware.

---

## The idea in one picture

```text
one sequential computation
        |
        v
decompose into independent tasks
        |
        v
Go goroutines + chan
        |
        v
parallel execution on cores of one machine
        |
        v
NetChan
        |
        v
workers on other machines and their cores
        |
        v
distributed worker pool
```

And each task is either:

```text
Fire-and-Forget: Work
Managed Task:    Work + Request + Reply
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

In protocol v2, a network channel can carry ordinary values and directional
channels inside messages. This allows a message to create temporary task-scoped
communication paths such as `Request` and `Reply`, as well as longer-lived
session channels.

It preserves message order, provides backpressure, recovers from temporary
disconnections, and distinguishes receipt by the remote node from an actual read
by the remote goroutine.

For task scheduling, the v2 model has two application-level forms:

```text
Fire-and-Forget: Work
Managed Task:    Work + Request + Reply
```

A managed task's `Request` and `Reply` channels are created specifically for that
task, live for that task's lifetime, and are never reused for the next task.

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
| Tasks had no network-native communication lifecycle | Managed tasks can carry task-scoped `Request` and `Reply` channels |
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

This is the mechanism used for task-scoped `Request` / `Reply` communication and
for longer-lived session channels without requiring a global client table or
shared mutable state.

A managed task creates a fresh pair for that task. The pair is not reused by the
next task.

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

## Managed task example

A managed task carries both directions:

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

The exact request and reply message types are application-defined. A simple
application may use `Request` only for cancellation and `Reply` only for the final
result; a richer application may carry progress, updates, parameter changes, or
status messages.

## Long-lived session channel

Task-scoped `Request` / `Reply` channels normally end with their task. A different
application may deliberately transfer a longer-lived session channel and keep it
in a dedicated goroutine until either side closes it.

```go
type Subscription struct {
    Events chan<- string
    Done   <-chan struct{}
}
```

The important distinction is scope: task channels belong to one task; session
channels may deliberately outlive an individual task.

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
scenarios involving temporary disconnections, distributed worker pools, managed
tasks, and channels inside messages are especially useful.

## Related projects

- [Netchan old version](https://github.com/matveynator/netchan-old) — an extension of Rob Pike's original idea;
- [Docker Libchan](https://github.com/docker/libchan) — a network message-passing interface;
- [GraftJS/jschan](https://github.com/graftjs/jschan) — a similar channel model for JavaScript;
- [Mat Ryer/Vice](https://github.com/matryer/vice) — Go channels in a distributed environment.

## License

NetChan is distributed under the [BSD 3-Clause License](LICENSE).
