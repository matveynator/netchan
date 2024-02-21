package netchan

import (
	"encoding/gob"
	"io"
	"log"
	"net"
	// "time"
)

// handleConnection manages a single client connection.
// It receives and sends messages using the send and receive channels.
// In case of disconnection, it notifies through the clientDisconnectNotifyChan.
// The function uses goroutines to concurrently handle incoming and outgoing messages.
func handleConnection(conn net.Conn, send chan Message, receive chan Message, clientDisconnectNotifyChan chan string) {

	// This deferred function notifies about the client disconnection and closes the connection.
	defer func() {
		clientDisconnectNotifyChan <- conn.RemoteAddr().String()
		conn.Close()
	}()

	// Channel to collect any errors that occur during connection handling.
	decodeErrorChannel := make(chan error, 1000)

	// Creating a new decoder and encoder for the connection.
	decoder := gob.NewDecoder(conn)
	encoder := gob.NewEncoder(conn)

	// Goroutine for receiving messages.
	go func() {
		for {
			var msg Message
			err := decoder.Decode(&msg)
			if err != nil {
				if err == io.EOF {
					// Connection closed by the other end.
					log.Printf("Error: remote connection closed. (%s)", err)
					decodeErrorChannel <- err
					return
				} else {
					// Send error to decodeErrorChannel and log it.
					decodeErrorChannel <- err
					log.Printf("Error while decoding: %s", err)
					return
				}
			}
			// Update the message with the sender's address and send it to the receive channel.
			msg.From = conn.RemoteAddr().String()
			receive <- msg
		}
	}()

	// Main loop for handling sending messages and connection errors.
	for {
		select {
		case message, ok := <-send:
			// Check if the send channel is closed.
			if !ok {
				log.Println("Exiting due to SEND channel closed.")
				return
			}

			// Attempt to encode and send the message.
			sendingErr := encoder.Encode(message)
			if sendingErr != nil {
				// Re-queue the message on failure and log the error.
				send <- message
				log.Printf("Re-queue sending data as sending failed with error: %s\n", sendingErr)
				return
			}
			// Logging the sent message is disabled to reduce verbosity.

		case decodeError := <-decodeErrorChannel:
			// Log any network error received and exit the loop.
			log.Printf("Netchan handle connection worker exited due to decode error: %s\n", decodeError)
			return
		}
	}
}
