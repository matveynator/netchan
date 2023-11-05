package netchan

import (
	"log"
	"net"
	"time"
)

// handleConnection deals with incoming messages on a network connection.
func handleConnection(conn net.Conn, netchan <-chan NetChan) {
	defer conn.Close() // Ensures the connection is closed to prevent resource leaks.

	for {
		select {
		case message, ok := <-netchan:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		default:
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}
}
