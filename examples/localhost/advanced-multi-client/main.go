package main

import (
	"github.com/matveynator/netchan" // Importing the netchan package for network channel functionalities.
	"log"                            // Importing the log package for logging messages to the console.
	"time"                           // Importing the time package for handling time-related functionality.
)

// main is the entry point of the program.
// It starts server and client goroutines and keeps the application running.
func main() {
	go server() // Starting the server in a new goroutine.
	go client() // Starting the first client in a new goroutine.
	go client() // Starting the second client in a new goroutine.

	// Using an empty select to block the main goroutine indefinitely.
	// This is necessary because the program should not exit to allow server and client goroutines to run.
	select {}
}

// server function sets up and runs the server side of the network channel.
// It listens for messages from clients and sends back responses.
func server() {
	// Initializing the network channel for server-side communication.
	send, receive, err := netchan.AdvancedListen("127.0.0.1:9999")
	if err != nil {
		log.Fatal(err) // Logging and exiting the program in case of an error.
		return
	}

	// Running an infinite loop to listen for and handle messages.
	for {
		select {
		case message := <-receive:
			log.Printf("Server received: %v\n", message) // Logging received messages.

			// Sending an echo response back to the client.
			myAddress := message.To
			message.To = message.From
			message.From = myAddress
			send <- message // Sending the response.
		}
	}
}

// client function sets up and runs the client side of the network channel.
// It periodically sends messages to the server and handles responses.
func client() {
	// Initializing the network channel for client-side communication.
	send, receive, err := netchan.AdvancedDial("127.0.0.1:9999")
	if err != nil {
		log.Println(err) // Logging the error but continuing execution.
	}

	// Launching a goroutine to send messages at regular intervals.
	go func() {
		for {
			// Creating and sending a message with a timestamp payload.
			data := netchan.Message{
				Payload: time.Now().UnixNano(),
				To:      "127.0.0.1:9999",
			}

			send <- data                          // Sending the message to the server.
			log.Printf("Client sent: %v\n", data) // Logging the sent message.

			time.Sleep(3 * time.Second) // Waiting for 3 seconds before sending the next message.
		}
	}()

	// Running an infinite loop to listen for and handle responses from the server.
	for {
		select {
		case message := <-receive:
			log.Printf("Client received: %v\n", message) // Logging received messages.
		}
	}
}
