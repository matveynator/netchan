package netchan

import (
	"log"
	"net"
	"time"
	"io"
)

// handleConnection deals with incoming messages on a network connection.
func handleConnection(conn net.Conn, send chan<- NetChanType, receive <-chan NetChanType) {

	defer conn.Close() // Ensures the connection is closed to prevent resource leaks.

	for {

		// Receiving data through network channel:

    // Create a buffer to read from the connection
    buffer := make([]byte, 1024)
    length, err := conn.Read(buffer)
    if err != nil {
        if err != io.EOF {
            log.Printf("Read error: %v", err)
        }
        break
    }

    // Process the received data
		log.Printf("Received data: %s\n", string(buffer[:length]))


		select {
		case message, ok := <-receive:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		default:
			log.Printf("handle connection sleeping...")
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}
}
