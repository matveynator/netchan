# “Quantum” Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Why NetChan

A common way to make a large computation finish sooner is to split it into smaller independent **sequential tasks**.

For example, instead of processing one huge range from beginning to end:

```text
one large task
0 ------------------------------------------------------ 1,000,000
```

we can split it into smaller pieces:

```text
Task 1:   0 - 99,999
Task 2:   100,000 - 199,999
Task 3:   200,000 - 299,999
Task 4:   300,000 - 399,999
...
```

Each piece is still ordinary sequential code:

```go
for i := start; i < end; i++ {
    check(i)
}
```

The important property is that the pieces are independent. Because they do not depend on each other, they can run at the same time.

### First: scale across CPU cores inside one computer

The first level of parallelism is local.

If one computer has several CPU cores, different sequential tasks can run on different cores:

```text
ONE COMPUTER

CPU Core 1 -> Task A
CPU Core 2 -> Task B
CPU Core 3 -> Task C
CPU Core 4 -> Task D
```

Instead of one core doing this:

```text
Task A -> Task B -> Task C -> Task D
```

several cores can do this:

```text
Task A -------->
Task B -------->
Task C -------->
Task D -------->
```

This is already useful parallelism: the individual tasks remain sequential, but several independent sequential tasks execute at the same time.

Go is especially convenient for this model because goroutines, channels, and `select` make it natural to organize many independent workers.

But local parallelism has a physical limit: one computer has only so many CPU cores.

### Then: scale across several computers

When all useful cores of one machine are busy, the next step is to add cores from other machines.

```text
Machine A

Core 1 -> Task A
Core 2 -> Task B
Core 3 -> Task C
Core 4 -> Task D


Machine B

Core 1 -> Task E
Core 2 -> Task F
Core 3 -> Task G
Core 4 -> Task H


Machine C

Core 1 -> Task I
Core 2 -> Task J
Core 3 -> Task K
Core 4 -> Task L
```

Now the computation is no longer limited to the cores of one computer.

The machines may be in the same rack, another datacenter, another city, or another continent. Each machine can run the same worker logic and execute ordinary sequential tasks.

Conceptually:

```text
large problem
     |
     v
split into independent sequential tasks
     |
     +--> CPU core on Machine A
     |
     +--> CPU core on Machine A
     |
     +--> CPU core on Machine B
     |
     +--> CPU core on Machine C
     |
     +--> CPU core on Machine D
```

This is what NetChan is for.

A native Go channel belongs to one Go runtime. It cannot directly connect a goroutine on Machine A with a goroutine on Machine B.

**NetChan extends the same channel-oriented model across the network, so workers on other computers can join the same computation.**

In simple terms:

```text
Go:
independent sequential tasks
running in parallel on CPU cores of one machine

NetChan:
the same model,
but CPU cores from other machines
can join the worker pool
```

NetChan does not make one CPU instruction magically faster. It expands the pool of execution resources available to the same task-oriented design.

---

## Shared task channel

A simple distributed architecture uses one shared task channel.

Workers wait for work:

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

A worker may be:

- a goroutine using another local CPU core;
- the same worker program on another machine;
- a server in another datacenter;
- a machine on another continent.

When there is no work, the worker simply blocks on the task channel.

```text
wait for task
     |
     | no task
     +-------------------- blocked
     |
     | task arrives
     v
receive task
     |
     v
execute sequential work
     |
     v
finish
     |
     v
wait for next task
```

The same mental model works locally and across the network.

---

# Two task modes

NetChan tasks have two simple modes:

```text
1. Fire-and-Forget

2. Managed Task
```

The difference is whether the sender needs any communication with the task after it has been dispatched.

---

## 1. Fire-and-Forget

The simplest task contains only the work itself.

```go
type Task struct {
    Work Work
}
```

The sender publishes it:

```text
Task Owner --------------------> Worker
                 Work
```

and forgets about it.

The worker receives the task, performs the sequential work, and finishes.

```text
send task
    |
    v
worker receives task
    |
    v
execute
    |
    v
finish
```

No task-local communication channels are created.

This mode is appropriate when the sender does not need:

- a result;
- cancellation;
- progress information;
- additional instructions;
- clarification during execution.

The model is simply:

```text
send -> execute -> forget
```

---

## 2. Managed Task

If the sender needs a result **or** wants to control the task while it is running, the task is managed.

A managed task always creates **two task-scoped directional channels**:

```text
Request
Reply
```

Never only one.

Conceptually:

```go
type ManagedTask struct {
    Work    Work
    Request <-chan Request
    Reply   chan<- Reply
}
```

The directions are always named from the point of view of the **task owner**, the side that created and dispatched the task:

```text
Task Owner                         Worker

          Work --------------------->

          Request ------------------->

          <---------------------- Reply
```

`Request` flows from the task owner to the worker.

`Reply` flows from the worker back to the task owner.

Each channel is simplex. Together they create a temporary duplex session for exactly one task.

---

## Request: owner -> worker

The `Request` channel is the control path from the task owner to the worker.

It can be used for things such as:

```text
Stop
Cancel
ChangePriority
UpdateParameters
ChangeRange
RequestProgress
ClarifyWork
```

A worker can observe it naturally with `select` while continuing its sequential computation:

```go
for candidate := start; candidate < end; candidate++ {
    select {
    case request, open := <-task.Request:
        if !open {
            // The task owner ended this managed task.
            return
        }

        handleRequest(request)

    default:
    }

    check(candidate)
}
```

If cancellation is all that is needed, the owner can close the request side.

The worker observes the closed receive channel and stops:

```text
Task Owner
    |
    | close Request
    v
 Worker
    |
    v
stop current computation
    |
    v
discard unfinished work
    |
    v
return to worker pool
```

This follows ordinary Go semantics: a receive from a closed channel is immediately selectable.

---

## Reply: worker -> owner

The `Reply` channel is the return path.

The worker can use it for:

```text
Result
Progress
Status
PartialResult
Error
```

The simplest case is a final result:

```go
task.Reply <- Reply{
    Result: result,
}
```

Conceptually:

```text
Worker
   |
   | result
   v
 Reply
   |
   v
Task Owner
```

The important distinction is that `Reply` is for returning information, not for discovering whether the task is still wanted.

A sender cannot safely do this with a send-only Go channel:

```text
check whether Reply is open
        |
        v
send to Reply
```

because another goroutine may close the channel between the check and the send.

That is why a managed task has the opposite-direction `Request` channel as well.

`Request` carries task lifetime and control. `Reply` carries information back.

---

## Zero channels or two channels

The task model is deliberately binary.

```text
FIRE-AND-FORGET

Work
```

or:

```text
MANAGED TASK

Work
+ Request
+ Reply
```

There is no managed form with only `Request`, and there is no managed form with only `Reply`.

If communication is required, both directions exist.

This keeps the lifecycle predictable and avoids making one channel perform two conflicting jobs.

---

## Request and Reply are task-scoped

The `Request` / `Reply` pair belongs only to the task that created it.

The pair is temporary and is never reused for the next task.

```text
Task A
 |
 +-- Work A
 +-- Request A
 +-- Reply A

Task A finishes
 |
 +-- Request A ends
 +-- Reply A ends


Task B
 |
 +-- Work B
 +-- Request B
 +-- Reply B
```

Task B gets a completely new pair.

The lifetime is therefore:

```text
Task created
    |
    +--> Request created
    +--> Reply created
    |
    v
Task executes
    |
    v
Task completes or is cancelled
    |
    +--> Request ends
    +--> Reply ends
```

The same `Request` and `Reply` channels are never recycled for another logical task.

This makes them **task-scoped capabilities**: they exist exactly for the lifetime of one managed task.

---

## Why this is useful

The shared task channel distributes work.

The task-local `Request` / `Reply` pair manages one particular piece of work after a worker has taken it.

```text
shared task channel
       |
       v
+------------------------+
| Managed Task           |
|                        |
| Work                   |
| Request -------------->+------> Worker control
| Reply   <--------------+<------ Worker result
+------------------------+
```

So there are two levels:

```text
Shared channel
    =
where workers obtain tasks

Task-scoped Request / Reply
    =
communication for one running task
```

---

## Competitive execution

A scheduler does not have to assign exactly one worker to every logical task.

Suppose there are:

```text
10 logical tasks
100 available workers
```

The scheduler may let several workers compete on the same important logical task.

```text
Task A -> Worker 01
Task A -> Worker 02
Task A -> Worker 03
...
```

All copies belong to the same managed task session and may share its task-scoped control/result capabilities.

```text
Worker 01 -------\
Worker 02 --------\
Worker 03 ---------> first valid Reply wins
Worker 04 --------/
Worker 05 -------/
```

When the first acceptable result arrives through `Reply`, the task owner can terminate the remaining work through `Request`.

For cancellation, closing `Request` is enough:

```text
first valid result
       |
       v
Task Owner closes Request
       |
       +-------> Worker 01 stops
       |
       +-------> Worker 02 stops
       |
       +-------> Worker 04 stops
       |
       +-------> Worker 05 stops
```

The losing workers abandon obsolete work and return to the common pool.

This is a simple form of distributed speculative execution.

---

## Decomposition instead of competition

Competition is only one strategy.

Often the better strategy is to make many smaller independent tasks.

```text
0 ------------------------------------------------------ 100,000,000
```

can become:

```text
Task 1:   0 - 999,999
Task 2:   1,000,000 - 1,999,999
Task 3:   2,000,000 - 2,999,999
Task 4:   3,000,000 - 3,999,999
...
```

and then:

```text
Machine A / Core 1 -> Task 1
Machine A / Core 2 -> Task 2
Machine B / Core 1 -> Task 3
Machine B / Core 2 -> Task 4
Machine C / Core 1 -> Task 5
```

Each CPU core still executes ordinary sequential code.

The speedup comes from running many independent sequential tasks at the same time.

Fast machines naturally finish more tasks. Slow machines naturally finish fewer.

---

## Heterogeneous workers

A distributed worker pool does not need identical hardware.

It may contain:

```text
an old laptop
a desktop CPU
a 64-core server
an ARM machine
a cloud VM
a GPU worker
a storage-heavy machine
```

Workers can maintain performance statistics such as:

```text
operations / second
iterations / second
average task runtime
factoring speed
encoding speed
rendering speed
```

A scheduler can use those measurements as an application-level policy when deciding how large a task should be or where it should run.

For example:

```text
Task target: < 500 ms

Worker A estimate: 8.2 s   -> unsuitable
Worker B estimate: 190 ms  -> execute
```

NetChan supplies the communication model; scheduling policy belongs to the application.

---

## Example workloads

This model is useful whenever a large problem can be decomposed into independent work.

Examples include:

- factoring and mining-style workloads;
- large search spaces;
- rendering;
- video and media processing;
- compression;
- scientific simulations;
- Monte Carlo workloads;
- crawling and indexing;
- distributed builds;
- testing and fuzzing;
- batch data processing;
- independent AI inference jobs.

Workers do not have to represent only CPU cores. They may also represent GPUs, memory-heavy machines, storage nodes, network resources, accelerators, or external hardware.

---

## The idea in one picture

First, scale inside one computer:

```text
ONE MACHINE

Core 1 -> sequential task A
Core 2 -> sequential task B
Core 3 -> sequential task C
Core 4 -> sequential task D
```

Then NetChan lets the same model grow beyond that machine:

```text
Machine A / Core 1 -> sequential task A
Machine A / Core 2 -> sequential task B

Machine B / Core 1 -> sequential task C
Machine B / Core 2 -> sequential task D

Machine C / Core 1 -> sequential task E
Machine C / Core 2 -> sequential task F

Machine D / Core 1 -> sequential task G
...
```

So the system grows naturally:

```text
one sequential computation
        |
        v
many independent sequential tasks
        |
        v
many CPU cores on one machine
        |
        v
NetChan
        |
        v
CPU cores on many machines
        |
        v
distributed worker pool
```

And each distributed task uses one of two modes:

```text
Fire-and-Forget
    Work

Managed Task
    Work + Request + Reply
```

The programming model remains close to ordinary Go:

```text
goroutines
channels
select
close
```

The difference is that the workers may now live on completely different computers.

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
