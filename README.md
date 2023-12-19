[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# Network channels in Golang, also referred to as "netchan," originally conceptualized by Rob Pike. 
Secure by default. Ready for clusters. Use standard Go channels for communication across different machines. Capable of sending and receiving various data types, including channels. Limited synchronization capability.

> Please note: This project is UNDER CONSTRUCTION as of (19 October 2023) - use only at your own risk.

<p align="right">
<img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>

## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

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
> Currently, the send operation in our system is non-blocking, meaning messages are sent immediately without delay. This can lead to potential message loss in case of network issues. Also, every SEND channel in netchan has a buffer size of one. Future updates will allow customization of this buffer size, but this feature is still in the testing phase. As a Golang programmer, it's crucial to keep these aspects in mind for effective and reliable message handling in network communications.

```go
send <- message
```

This basic example demonstrates how to set up a simple server-client communication using `netchan`. Remember to handle errors appropriately and ensure that your network addresses and ports are configured correctly for your specific use case.

## Understanding the Key Differences Between Netchan and Go Channels

netchan is  described as 'Go channels over the network,' but this is a bit misleading for beginners. In Go, channels are used for both communicating between processes and synchronizing them. However, this early version of netchan is designed primarily for communication, not synchronization. This distinction is important to understand: while Go channels help coordinate process timing, netchan focuses on sending and receiving data across networks. We're aiming to include both communication and synchronization in netchan eventually, but currently, its synchronization capability isn't fully developed or tested.

## Documentation
- [version 1.0 Plan and goals](wiki/v1-plan.md)

## Benchmark 

```
Benchmark netchan (TLS 1.3 + GOB Encode/Decode) via localhost:9999

Intel(R) Core(TM) m3-7Y32 CPU @ 1.10GHz 1 core:
===============================================
Sent:                  1092349 (33101 msg/sec) - 3677 msg/sec per client
Received:              1092340 (33100 msg/sec) - 3677 msg/sec per client
Processed:             2184672 (64263 msg/sec)
Not received:          10 msg in 33 seconds
Successfully connected 9 clients

Sent:                  2713953 (35709 msg/sec) - 743 msg/sec per client
Received:              2713911 (35709 msg/sec) - 743 msg/sec per client
Processed:             5427864 (70830 msg/sec)
Not received:          42 msg in 76 seconds
Successfully connected 48 clients

Sent:                  27225572 (28448 msg/sec) - 8 msg/sec per client
Received:              27223530 (28446 msg/sec) - 8 msg/sec per client
Processed:             54449102 (56838 msg/sec)
Not received:          2042 msg in 957 seconds
Successfully connected 3214 clients

Intel(R) Core(TM) i7-3930K CPU @ 3.20GHz 4 core:
================================================
Sent:                  4492383 (109570 msg/sec) - 3424 msg/sec per client
Received:              4492388 (109569 msg/sec) - 3424 msg/sec per client
Processed:             8984727 (214121 msg/sec)
Not received:          19 msg in 41 seconds
Successfully connected 32 clients

AMD Ryzen 9 7950X3D 16-Core Processor:
================================================
Sent:                  29669916 (463592 msg/sec) - 11886 msg/sec per client
Received:              29669902 (463592 msg/sec) - 11886 msg/sec per client
Processed:             59339805 (912741 msg/sec)
Not received:          17 msg in 64 seconds
Successfully connected 39 clients

Sent:                  32905716 (353824 msg/sec) - 36 msg/sec per client
Received:              32905698 (353824 msg/sec) - 36 msg/sec per client
Processed:             65811411 (699962 msg/sec)
Not received:          21 msg in 93 seconds
Successfully connected 9708 clients

Sent:                  25696827 (333725 msg/sec) - 17 msg/sec per client
Received:              25696812 (333724 msg/sec) - 17 msg/sec per client
Processed:             51393629 (658509 msg/sec)
Not received:          21 msg in 77 seconds
Successfully connected 19488 clients
```

## Community and Support
  Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
  `netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

