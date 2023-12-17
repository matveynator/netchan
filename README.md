[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# netchan = go network channels. 
> Secure by default. Cluster ready. Communicate via standard go channels across multiple machines. Can send and receive any type of data including another channels.

<p align="right">
<img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>

## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## Usage example:
Please note that message could be any type of data including chan (go channel).

### Import:
`import ( "github.com/matveynator/netchan" )`

### Create SERVER:
`send, receive, err := netchan.Listen("127.0.0.1:9999")`

### Create CLIENT:
`send, receive, err := netchan.Dial("127.0.0.1:9999")`

### Receive message from server -> client OR from client -> server:
`message := <-receive`

### Send message from server -> client OR from client -> server:
`send <- message`

## Documentation
- [version 1.0 Plan and goals](wiki/v1-plan.md)

## Community and Support
  Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
  `netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

