[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# Welcome to the netchan Wiki!

<p align="right">
    <img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>


## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## Getting Started
To embark on your journey with `netchan`, install the library using `go get`:

`go get -u github.com/matveynator/netchan`

## Usage Example:

```
package main

import (
  "fmt"
  "github.com/matveynator/netchan"
  "time"
)

func main() {
  go server()

  go client()
  go client()

  for {
    time.Sleep(1 * time.Second)
  }
}

// server
func server() {
  send, receive, err := netchan.Listen("127.0.0.1:9999")
  if err != nil {
    fmt.Println(err)
  }

  //receiving and sending goroutine
  for {
    select {
    case message := <-receive:
      fmt.Printf("Server received: %s\n", message)
      //Returning received message to any ready client.
      send <- message
    }
  }
}

func client() {
  send, receive, err := netchan.Dial("127.0.0.1:9999")
  if err != nil {
    fmt.Println(err)
    return
  }

  //sending goroutine
  go func() {
    for {
      message := fmt.Sprintf("Current unixtime: %d", time.Now().UnixNano())
      send <- message
      fmt.Printf("Client sent: %s\n", message)
      time.Sleep(3 * time.Second)
    }
  }()

  //receiving goroutine
  for {
    select {
    case message := <-receive:
      fmt.Printf("Client received: %v\n", message)
    }
  }
}
```

## Documentation
- [version 1.0 Plan and goals](wiki/v1-plan.md)

## Community and Support
Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
`netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

