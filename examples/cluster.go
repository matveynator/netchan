// Package main demonstrates the use of the netchan package for creating a simple cluster.
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
// It sends messages to clients and they echoe them back to server.
func server() {
	send, receive, err := netchan.Listen("127.0.0.1:9999")

	if err != nil {
		log.Println(err) // Logging any error encountered during setup.
		return
	}

	// Goroutine that sends messages to connected clients.
	go func() {
		for {
			// Create new message with current time:
			message := time.Now().UnixNano()

			// Sending message to clients:
			send <- message

			// Log message:
			log.Printf("Server sent: %d\n", message)

			// Sleep 100 Millisecond before next message:
			time.Sleep(100 * time.Millisecond)
		}
	}()

	// Server's loop for handling incoming messages.
	for {
		select {
		case message := <-receive:
			//print message
			log.Printf("Server received: %v\n", message)
		}
	}
}

// client function manages the client-side operations of the application.
// It receives messages from the server and echo them back.
func client() {
	send, receive, err := netchan.Dial("127.0.0.1:9999")

	if err != nil {
		log.Println(err) // Logging any error encountered during connection.
	}

	// Client's loop for handling incoming messages from server:
	for {
		select {
		case message := <-receive:
			log.Printf("Client received: %v\n", message)
			// Echo message back to server (this could be a completed task)
			send <- message
		}
	}
}
