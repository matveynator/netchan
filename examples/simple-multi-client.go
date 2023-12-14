// Package main demonstrates the use of the netchan package for creating a simple
// client-server application. This application includes a server and a client, where
// the client sends random messages to the server, and the server echoes them back.
package main

import (
	"github.com/matveynator/netchan" // netchan package for network channel functionalities.
	"log"                            // log package for logging messages to the console.
	"time"                           // time package for handling time-related functionality.
)

// main is the entry point of the application. It concurrently starts the server
// and the client as separate goroutines, allowing them to operate simultaneously.
func main() {

	go server() // Launch server as a goroutine.

	go client() // Launch client as a goroutine.
	go client() // Launch client as a goroutine.
	go client() // Launch client as a goroutine.
	go client() // Launch client as a goroutine.
	go client() // Launch client as a goroutine.

	select {}
}

// server function manages the server-side operations of the application.
// It continuously listens for incoming messages and sends back echo responses.
func server() {
	send, receive, err := netchan.Listen("127.0.0.1:9999")
	if err != nil {
		log.Println(err)
		return
	} else {

		//receiving and sendin back goroutine
		for {
			select {
			case message := <-receive:
				log.Printf("Server received: %v\n", message)
				// Resending received message to any ready client.
				send <- message
			}
		}

	}
}

func client() {
	send, receive, err := netchan.Dial("127.0.0.1:9999")

	if err != nil {
		log.Println(err)
	}

	//sending goroutine
	go func() {
		for {
			unixTime := time.Now().UnixNano()
			send <- unixTime
			log.Printf("Client sent: %d\n", unixTime)
			time.Sleep(3 * time.Second) // Pausing for 3 seconds before sending the next message.
		}
	}()

	//receiving goroutine
	for {
		select {
		case message := <-receive:
			log.Printf("Client received: %v\n", message)
		}
	}
}
