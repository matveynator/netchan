[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# netchan: go network channels. 
Secure by default. Cluster ready. Communicate via standard go channels across multiple machines. Can send any type of data including another channels.

<p align="right">
<img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>


## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## Getting Started
To embark on your journey with `netchan`, install the library using `go get`:

`go get -u github.com/matveynator/netchan`

## Usage Example (simple cluster):

```
// Example demonstrates the use of the netchan package for creating a simple cluster.
package main

import (
	"log"
	"time"

	//add netchan import:
	"github.com/matveynator/netchan"
)

// respawnLock is a channel used to control the spawning of client routines.
var respawnLock chan int

func main() {
	//start 1 server:
	go server()

	//start 50 clients:
	respawnLock = make(chan int, 50)
	// Launches a goroutine that periodically tries to run dialWorkerRun.
	go func() {
		for {
			respawnLock <- 1
			go client()
		}
	}()

	// Loop forever.
	select {}
}

// server function manages the server-side operations of the application.
// It sends messages (tasks) to clients and they echoe (compute) them back to server.
func server() {
	send, receive, err := netchan.Listen("127.0.0.1:9999")
	if err != nil {
		log.Println(err)
		return
	} else {
		// Goroutine that sends messages to connected clients in our cluster.
		go func() {
			for {
				// Create new message with current time (just a simple text for our example):
				message := time.Now().UnixNano()

				// Sending message to clients (this could be anything, for example task to factorize big number):
				send <- message

				// Log message:
				log.Printf("Server sent: %d\n", message)

				// Sleep 50 Millisecond before next message:
				time.Sleep(50 * time.Millisecond)
			}
		}()

		// Server's loop for handling incoming messages.
		for {
			select {
			case message := <-receive:
				//Receiving results from our cluster:
				log.Printf("Server received: %v\n", message)
			}
		}
	}
}

// client function manages the client-side operations of the application.
// It receives messages from the server and echo them back.
func client() {
	send, receive, err := netchan.Dial("127.0.0.1:9999")
	if err != nil {
		log.Println(err)
		return
	} else {
		// Client's loop for handling incoming messages (tasks) from server:
		for {
			select {
			case message := <-receive:
				log.Printf("Client received: %v\n", message)
				// Echo message back to server (this could be a computed task)
				send <- message
			}
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

