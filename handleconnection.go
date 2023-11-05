// Package netchan is presumably a custom package designed to work with network channels.
package netchan

// Importing necessary packages:
// "log" is used for logging error or info messages.
// "net" provides a portable interface for network I/O, including TCP/IP, UDP, domain name resolution, and Unix domain sockets.
import (
	"log"
	"net"
	"time"
)

// handleConnection is a function that takes a net.Conn interface and a receive-only channel of NetChan type.
// It encapsulates the logic needed to handle a single network connection.
func handleConnection(conn net.Conn, netchan <-chan NetChan) {
	// "defer conn.Close()" ensures that the connection will be closed when the function returns,
	// as a cleanup action to avoid leaking resources.
	defer conn.Close()

	// Starting an infinite for loop to handle the incoming messages continuously.
	for {
		// The select statement is used for choosing which of the communication operations can proceed.
		select {
		// Here we are waiting to receive a message from the netchan.
		// If a message is received, it is assigned to the variable "message",
		// and the boolean "ok" will be true if the channel is not closed.
		case message, ok := <-netchan:
			// If "ok" is false, it means the channel has been closed.
			if !ok {
				// Therefore, we log that the main netchan channel was closed and exit the function.
				log.Println("main netchan channel closed, exiting")
				return
			}
			// If the channel is still open and a message is received, it is printed out.
			// The message structure is assumed to have id, secret, and data fields.
			log.Printf("Received netchan message: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		// The default case is executed if no other case is ready,
		// i.e., if there are no messages on the netchan to be read.
		// This ensures that the loop is non-blocking.
		default:
			// The program waits for a second before the next iteration of the loop.
			// This prevents the CPU from being used excessively when there is nothing to process.
			time.Sleep(1 * time.Second)
		}
	}
}

