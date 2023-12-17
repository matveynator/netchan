[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# Network channels in Golang, also referred to as "netchan," originally conceptualized by Rob Pike. 
> Secure by default. Ready for clusters. Use standard Go channels for communication across different machines. Capable of sending and receiving various data types, including channels.

<p align="right">
<img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>

## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## netchan Usage Example

> This guide provides a basic example of how to use the `netchan` package for setting up simple server-client communication in Go. Note that `message` can be any type of data, including a Go channel (`chan`).

### Step 1: Import the Package
First, import the `netchan` package into your Go program.

```go
import (
    "github.com/matveynator/netchan"
)
```

### Step 2: Create a Server
Set up a server that listens on a specified IP address and port. Handle any errors that might occur during this process.

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
> Please note that currently send operation is non-blocking and there's a possibility of losing messages, even without network or process failures.
```go
send <- message
```

This basic example demonstrates how to set up a simple server-client communication using `netchan`. Remember to handle errors appropriately and ensure that your network addresses and ports are configured correctly for your specific use case.


## Documentation
- [version 1.0 Plan and goals](wiki/v1-plan.md)

## Community and Support
  Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
  `netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

