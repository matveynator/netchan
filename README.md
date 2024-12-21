# "Quantum" Network Channels in Go

[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

## Introduction

Go channels are widely known for their simplicity and power in managing concurrent tasks. Essentially, Go channels can be seen as "quantum" channels where data transmission mimics quantum properties: data disappears from the sender at the exact moment it appears at the receiver. This seamless interaction ensures synchronization and serves as the foundation for highly concurrent systems.

The `netchan` library aims to bring these "quantum" capabilities to the network level, enabling Go developers to use channel-like abstractions for machine-to-machine interaction. However, unlike Go’s native channels, `netchan` in its current implementation primarily focuses on data transmission and does not yet fully replicate the synchronization features of Go channels. Expanding `netchan` to achieve both data transmission and synchronization will be key to its full potential.

> **Note:** The project is under active development. Contributions and testing are welcome to continue refining and enhancing its capabilities.

## Why "Quantum" Network Channels?

In Go, native channels inherently synchronize data and processes. This is achieved through the Go runtime, which acts as a "hypervisor," managing data exchange with precision. Extending this concept to a networked environment is challenging due to the lack of an equivalent hypervisor ensuring synchronization without creating intermediate data copies.

While `netchan` currently enables data transfer across machines, it does not yet replicate the synchronization behavior found in native Go channels. This limitation presents a unique opportunity for innovation:

1. **Developing a Network Hypervisor**: Creating a system that guarantees seamless, synchronized data transfer between sender and receiver.
2. **Achieving True Quantum Behavior**: Mimicking Go’s channel synchronization at a network level, ensuring that data appears and disappears as if governed by a higher-order control mechanism.

## Overview

`netchan` is a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

For more details on implementation, refer to the [Documentation](wiki/README.md).

## netchan Usage Example

This guide provides a basic example of how to use the `netchan` package for setting up simple server-client communication in Go. Note that `message` can be any type of data, including a Go channel (`chan`).

### Step 1: Import the Package
First, import the `netchan` package into your Go program.

```go
import (
    "github.com/matveynator/netchan"
)
```

### Step 2: Create a Server
Set up a server that listens on a specified IP address and port. Handle any errors that might occur.

```go
send, receive, err := netchan.Listen("127.0.0.1:9876")
if err != nil {
    // handle error
}
```

### Step 3: Create a Client
Create a client that connects to the server using the same IP address and port.

```go
send, receive, err := netchan.Dial("127.0.0.1:9876")
if err != nil {
    // handle error
}
```

### Step 4: Receiving Messages
To receive a message, whether from server to client or vice versa, use the following code. It waits for a message on the `receive` channel.

```go
message := <-receive
// process message
```

### Step 5: Sending Messages
To send a message, either from the server to the client or in the opposite direction, use the `send` channel.

> Note: `netchan`'s send operation is non-blocking and sends messages instantly. However, network issues may lead to message loss. Each SEND channel in `netchan` has a one-message buffer. Buffer size customization is being tested and might be available later. Keep this in mind for reliable network application messaging.

```go
send <- message
```

This basic example demonstrates how to set up simple server-client communication using `netchan`. Remember to handle errors appropriately and ensure that your network addresses and ports are configured correctly for your specific use case.

## Current Limitations and Future Directions

While `netchan` excels at enabling cross-machine communication, its synchronization capabilities—a hallmark of Go channels—are still in development. Below are the key limitations and the roadmap for addressing them:

1. **Synchronization Features**:
   - Current implementation lacks synchronization at the network level, meaning it does not yet coordinate process timing or mutual exclusion like native Go channels.
   - Future versions aim to incorporate mechanisms that mimic the "quantum" synchronization properties of Go channels over the network.

2. **Network Hypervisor**:
   - There is no system in place to ensure that data is transmitted and received without intermediate copies.
   - Development of a network hypervisor will be critical for achieving true quantum behavior, enabling seamless, lossless data synchronization between sender and receiver.

3. **Scalability Enhancements**:
   - While `netchan` supports basic scalability, advanced use cases like large distributed systems will require further optimization and robust error handling.

## Benchmarks

```
Benchmark netchan (TLS 1.3 + GOB Encode/Decode) via localhost:9999

Intel(R) Core(TM) m3-7Y32 CPU @ 1.10GHz 1 core:
===============================================
Sent:                  1092349 (33101 msg/sec) - 3677 msg/sec per client
Received:              1092340 (33100 msg/sec) - 3677 msg/sec per client
Processed:             2184672 (64263 msg/sec)
Not received:          10 messages in 33 seconds
Successfully connected 9 clients

... (other benchmarks here)
```

## Community and Support

Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository. Contributions are always welcome as we aim to build a library that pushes the boundaries of networked communication in Go.

For general goals, package structure, and implementation details, visit the [General Documentation](wiki/README.md).

## Similar Projects

Here are some projects related to Go network channels:

- [Netchan (old version)](https://github.com/matveynator/netchan-old) - Rob Pike’s initial concept.
- [Docker Libchan](https://github.com/docker/libchan) - A lightweight, networked, message-passing interface from Docker.
- [GraftJS/jschan](https://github.com/GraftJS/jschan) - A JavaScript implementation of similar channel-based communication.
- [Matryer/Vice](https://github.com/matryer/vice) - Go channels at horizontal scale.

## License

`netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE).
