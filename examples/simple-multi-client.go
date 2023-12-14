// Package main demonstrates the use of the netchan package for creating a simple
// client-server application. This application includes a server and multiple clients,
// where each client sends random timestamps to the server, and the server echoes them back.
package main

import (
	"github.com/matveynator/netchan" // Importing netchan package for network channel functionalities.
	"log"                            // Importing log package for logging messages to the console.
	"time"                           // Importing time package for handling time-related functionalities.
)

// main is the entry point of the application. It concurrently starts the server
// and multiple clients as separate goroutines, allowing them to operate simultaneously.
func main() {

	go server() // Launching the server in its own goroutine to run concurrently.

	// Launching multiple clients in their own goroutines to run concurrently.
	// This allows for multiple client connections to the server simultaneously.
	go client()
	go client()
	go client()
	go client()
	go client()

	select {} // Blocking the main function to keep the application running.
}

// server function manages the server-side operations of the application.
// It listens for incoming messages from clients and echoes them back.
func server() {
	send, receive, err := netchan.Listen("127.0.0.1:9999") // Setting up the server to listen on localhost port 9999.

	if err != nil {
		log.Println(err) // Logging any error encountered during setup.
		return
	}

	// Server's loop for handling incoming messages.
	for {
		select {
		case message := <-receive:
			log.Printf("Server received: %v\n", message) // Logging received messages.
			send <- message                              // Echoing the received message back to the client.
		}
	}
}

// client function manages the client-side operations of the application.
// It sends timestamps to the server and receives echo responses.
func client() {
	send, receive, err := netchan.Dial("127.0.0.1:9999") // Connecting the client to the server at localhost port 9999.

	if err != nil {
		log.Println(err) // Logging any error encountered during connection.
	}

	// Goroutine for sending messages to the server.
	go func() {
		for {
			unixTime := time.Now().UnixNano() // Generating a timestamp.
			send <- unixTime                  // Sending the timestamp to the server.
			log.Printf("Client sent: %d\n", unixTime)
			time.Sleep(3 * time.Second) // Waiting for 3 seconds before sending the next message.
		}
	}()

	// Client's loop for handling incoming echoed messages.
	for {
		select {
		case message := <-receive:
			log.Printf("Client received: %v\n", message) // Logging the echoed message received from the server.
		}
	}
}
