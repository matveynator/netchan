# “Quantum” Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Why NetChan

A common way to speed up a large computation is to split it into smaller independent sequential tasks.

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

Each of these pieces is still an ordinary sequential task.

For example, one worker may simply execute:

```go
for i := start; i < end; i++ {
    check(i)
}
```

The important part is that these sequential tasks are independent. Because they do not depend on each other, they can be executed in parallel.

The first level of parallelism is inside one machine. If the CPU has several cores, different sequential tasks can run at the same time on different cores:

```text
one computer

CPU Core 1 -> Task 1
CPU Core 2 -> Task 2
CPU Core 3 -> Task 3
CPU Core 4 -> Task 4
```

So instead of executing:

```text
Task 1
then Task 2
then Task 3
then Task 4
```

we execute them at the same time:

```text
Task 1 -------->
Task 2 -------->
Task 3 -------->
Task 4 -------->
```

This is the first source of acceleration: several CPU cores execute independent sequential work in parallel instead of one core performing all pieces one after another.

Go is especially convenient for this style of programming because goroutines, channels, and `select` make it easy to organize many independent workers.

But at this point we are still limited by the hardware of one computer. If one machine has eight useful CPU cores and all eight are busy, there are no more local CPU cores to add.

The natural next step is to use CPU cores from other machines as well:

```text
Computer A

Core 1 -> Task A
Core 2 -> Task B
Core 3 -> Task C
Core 4 -> Task D


Computer B

Core 1 -> Task E
Core 2 -> Task F
Core 3 -> Task G
Core 4 -> Task H
```

Then another machine can join:

```text
Computer C

Core 1 -> Task I
Core 2 -> Task J
Core 3 -> Task K
Core 4 -> Task L
```

Now the same computation can use CPU resources belonging to many different computers.

Those computers do not have to be in the same rack, datacenter, country, or continent. Each machine can run the same Go worker program and the same sequential worker loop. The individual computations remain sequential; the acceleration comes from executing many independent sequential computations at the same time.

Conceptually:

```text
large problem
     |
     v
split into independent sequential tasks
     |
     +--> Task A -> CPU core on Machine 1
     |
     +--> Task B -> CPU core on Machine 1
     |
     +--> Task C -> CPU core on Machine 2
     |
     +--> Task D -> CPU core on Machine 3
     |
     +--> Task E -> CPU core on Machine 4
```

This is where ordinary Go channels reach a physical boundary.

A native Go channel belongs to one Go runtime. It cannot directly connect a goroutine running on Computer A with a goroutine running on Computer B.

**NetChan extends the same channel-oriented model across the network.**

In simple terms:

```text
Go:
parallel execution of independent sequential tasks
across CPU cores of one machine

NetChan:
the same model,
but CPU cores and workers from other machines
can join the same computation
```

NetChan does not make one CPU instruction magically faster. It makes additional execution resources available to the same concurrency-oriented design.

---

## Shared task channel

A natural NetChan architecture is one shared task channel.

All workers listen to the same channel and wait for work:

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

A worker may be a goroutine using another local CPU core, the same worker program on another physical machine, a server in another datacenter, or a machine on another continent.

Each free worker blocks while waiting for a task. It does not need to poll the scheduler continuously.

```text
wait for task
     |
     | no work
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
return result
     |
     v
wait for next task
```

The same mental model works whether the worker is nearby or across the network.

---

## A reply channel inside every task

Every task can carry its own temporary reply channel.

Conceptually:

```go
type Task struct {
    Work  Work
    Reply chan<- Result
}
```

The scheduler does not only send:

```text
"do this work"
```

It sends:

```text
"do this work,
and return the answer through this temporary channel"
```

This is possible because NetChan can transfer a directional channel inside another channel.

```text
shared task channel
        |
        v
+---------------------+
| Task                |
|                     |
| Work                |
| Reply channel ------+--------+
+---------------------+        |
                               v
                         return result
```

The temporary Reply channel belongs to one logical task.

The important v2 task model is that this same Reply channel also represents the lifetime of the task.

There is no separate per-task cancellation channel in this model.

```text
Task
 |
 +--> Work
 |
 +--> temporary Reply channel
```

The one temporary Reply channel means both:

```text
where the answer should go
```

and:

```text
whether anybody still needs the answer
```

As long as the Reply channel is alive, the task is still relevant.

If Reply is closed, the meaning is simple:

```text
nobody needs this result anymore
```

For example:

- another worker already found the answer;
- another machine completed the same logical task faster;
- the required block was already found;
- the parent computation completed;
- the scheduler cancelled the work;
- the input state changed;
- the requester disappeared.

So the temporary Reply channel is simultaneously:

```text
return path
+
task lifetime
+
cancellation signal
```

### Checking task lifetime

Long-running workers should periodically observe the lifecycle of the transferred Reply capability at sensible interruption points inside their sequential loop.

Conceptually:

```go
for candidate := start; candidate < end; candidate++ {
    if replyIsClosed(task.Reply) {
        return
    }

    result := calculate(candidate)

    if valid(result) {
        sendResult(task.Reply, result)
        return
    }
}
```

`replyIsClosed` and `sendResult` above describe the intended v2 Reply-lifecycle API semantics: observing remote Reply closure must be safe and must not require attempting a send to an already closed channel.

The worker logic is intentionally simple:

```text
Reply alive?
    |
 +--+--+
 |     |
yes    no
 |     |
 v     v
work   stop
```

If Reply remains alive, the worker continues computing.

If Reply is closed, the worker does not panic and does not continue wasting resources. It abandons the obsolete computation and immediately returns to the shared worker pool:

```text
Reply closed
     |
     v
stop current task
     |
     v
discard unfinished work
     |
     v
return to shared task channel
     |
     v
block until another task arrives
```

This makes the temporary Reply channel part of scheduling rather than merely a place to send an answer.

---

## Competitive execution

A scheduler does not have to assign exactly one worker to every logical task.

Suppose there are:

```text
10 logical tasks
100 available workers
```

One option is to leave 90 workers idle.

Another option is to deliberately publish multiple executions of the same logical task:

```text
Task A -> Worker 01
Task A -> Worker 02
Task A -> Worker 03
...
Task A -> Worker 10
```

All workers race on the same logical task.

```text
Worker 01 -------\
Worker 02 --------\
Worker 03 ---------> first valid result wins
Worker 04 --------/
Worker 05 -------/
```

The first valid answer is accepted. The scheduler then closes the temporary Reply lifecycle for that logical task.

The remaining workers observe that Reply is no longer alive and stop their copies:

```text
Reply closed
     |
     +-------> Worker 01 stops
     |
     +-------> Worker 02 stops
     |
     +-------> Worker 04 stops
     |
     +-------> Worker 05 stops
```

Those workers immediately return to the common pool and can be reused for other work.

This is a simple form of distributed speculative execution:

```text
run several competitors
        |
        v
accept the fastest valid result
        |
        v
close the task Reply lifecycle
        |
        v
stop all losing copies
        |
        v
reuse their resources
```

The same strategy can be used between cores of one machine. NetChan extends it to independent computers.

---

## Decomposition instead of competition

Competition is only one strategy.

Often the better strategy is to divide a large problem into increasingly smaller independent sequential tasks.

For example:

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

Different workers execute different parts:

```text
Machine A / Core 1 -> Task 1
Machine A / Core 2 -> Task 2
Machine B / Core 1 -> Task 3
Machine B / Core 2 -> Task 4
Machine C / Core 1 -> Task 5
```

When one worker finishes, it takes the next available task.

Fast machines naturally consume more jobs. Slow machines naturally consume fewer.

So the same architecture scales first across CPU cores and then across entire computers.

---

## Heterogeneous workers

A distributed pool does not need identical hardware.

It may contain:

```text
an old laptop
a desktop CPU
a 64-core server
an ARM machine
a cloud VM
a large cluster node
```

Each worker can maintain statistics about its own performance, for example:

```text
operations / second
iterations / second
average task runtime
factoring speed
hashing speed
encoding speed
rendering speed
```

A task may also carry estimated complexity or a desired completion time.

A weak worker may estimate:

```text
expected runtime: 12 s
target runtime: < 500 ms
```

and return or requeue the task instead of spending a long time on work that another machine can execute much faster.

A more powerful worker may estimate:

```text
expected runtime: 180 ms
```

and accept it.

This allows heterogeneous machines to cooperate in one pool while still assigning work efficiently.

---

## Example: mining and factoring

Mining and factoring workloads are easy examples of this model.

A large search or factoring problem can be decomposed into many independent sequential pieces.

Instead of:

```text
one CPU
   |
   v
calculate everything sequentially
```

we can have:

```text
                     mining/factoring problem
                              |
                        decompose work
                              |
          +-------------------+-------------------+
          |                   |                   |
          v                   v                   v
      Machine A           Machine B           Machine C
       Core 1              Core 1              Core 1
          |                   |                   |
     sequential          sequential          sequential
        task                task                task
```

More machines mean more CPU cores participating in the same computation.

The scheduler can either divide the search into different ranges or deliberately send the same important task to several workers and let them compete.

When one worker finds the required result, the temporary Reply lifecycle is closed and all unnecessary copies stop.

---

## It is not limited to CPU

The same model is useful whenever a larger problem can be decomposed into independent work.

Workers may represent access to:

- CPU cores;
- GPUs;
- memory-heavy machines;
- storage nodes;
- network bandwidth;
- rendering machines;
- video encoders;
- specialized accelerators;
- external hardware.

Typical workloads include:

- mining and factoring;
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

The common property is simple: the larger problem can be decomposed into independent sequential tasks.

---

## The idea in one picture

Ordinary local Go concurrency:

```text
ONE MACHINE

Core 1 -> sequential task A
Core 2 -> sequential task B
Core 3 -> sequential task C
Core 4 -> sequential task D
```

Distributed execution with NetChan:

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
many sequential tasks
        |
        v
many CPU cores
        |
        v
many CPUs
        |
        v
many machines
        |
        v
distributed concurrent system
```

The individual algorithms remain simple sequential code.

The power comes from executing many independent sequential tasks in parallel.

NetChan extends this idea from the cores available inside one computer to resources available across many computers, while keeping the programming model close to ordinary Go:

```text
goroutines
channels
select
close
```

The difference is that the workers may now live on completely different machines.

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

In protocol v2, a network channel can carry values, reply channels, and long-lived
session channels. It preserves message order, provides backpressure, recovers
from temporary disconnections, and distinguishes receipt by the remote node from
an actual read by the remote goroutine.

The v2 task model uses a transferred temporary Reply channel as both the result
path and the lifecycle of a logical task. Closing that Reply capability makes
remote workers stop obsolete copies of the task without requiring a separate
per-task `Done` channel.

NetChan still does not hide the physics of a distributed system: the network
requires encoding and a temporary copy, while two independent Go schedulers
cannot perform a single atomic `select`. These boundaries are represented by
explicit channels and protocol states rather than message loss or hidden shared
state.

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
| Primarily values could be transferred | Messages can carry directional reply and session channels |
| Tasks had no network-native lifecycle | A temporary transferred Reply capability is the task result path and lifecycle; closing it cancels obsolete work |
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
not accumulated values. This is the primary mechanism for reply channels and
long-lived session channels without a client table or shared mutable state.

For task scheduling, a temporary Reply capability also represents task lifetime:
closing it means that remote work associated with that logical task is obsolete.

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

## A channel inside a channel: task and single-reply lifecycle

Each task creates its own temporary reply channel. The handler needs no client
address, request identifier, or shared table of pending results.

```go
type Result struct {
    Text string
}

type Task struct {
    Text  string
    Reply chan<- Result
}
```

The requester creates one reply channel and sends it inside the task:

```go
func requestTask(connection *netchan.Channel[Task], text string) (Result, bool) {
    reply := make(chan Result)
    task := Task{Text: text, Reply: reply}

    select {
    case connection.Send <- task:
    case <-connection.Done:
        return Result{}, false
    }

    select {
    case result, open := <-reply:
        return result, open
    case <-connection.Done:
        return Result{}, false
    }
}
```

The task's Reply is intentionally more than a return address. It is the logical
lifetime of that task.

The v2 task lifecycle semantics are:

```text
Reply alive   -> task is relevant -> keep working
Reply closed  -> task is obsolete -> stop immediately
result found  -> send it to Reply
```

Long-running handlers observe the Reply lifecycle between computational steps.
The public convenience API for observing remote Reply closure is intended to be
safe and must not require a speculative send to a closed channel.

Conceptually:

```go
func handleTask(task Task) {
    for step := firstStep(task); ; step = nextStep(step) {
        if replyIsClosed(task.Reply) {
            return
        }

        result, found := calculateStep(step)
        if found {
            sendResult(task.Reply, result)
            return
        }
    }
}
```

The requester or scheduler closes Reply as soon as the result is no longer
needed. Every worker executing a copy of the same logical task observes that
closure, abandons the obsolete computation, returns to the common worker pool,
and blocks until another task arrives.

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
Closing the corresponding `Done` is the reliable terminal signal for the root
logical connection.

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

Protocol v2 cannot eliminate the physical properties of a distributed system:

- two computers do not share a goroutine scheduler, so an atomic cross-machine
  `select` is impossible;
- wire transport requires encoding and a temporary physical copy;
- a process that loses its memory cannot recover exactly-once delivery without
  application-level persistent storage;
- finite memory requires bounded queues and input-size validation.

NetChan makes these boundaries explicit through `Deliver`, root-channel `Done`,
`Errors`, nested-channel closure, backpressure, and logical session state.

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
