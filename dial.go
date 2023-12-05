// Package netchan provides network channel functionality for secure,
// sequenced communication between client and server. It implements a simple
// method for establishing TLS-secured TCP connections and facilitating
// message passing between communicating peers.
package netchan

import (
	"crypto/tls" // Package tls provides implementations of the TLS network security protocol.
	"fmt"        // Package fmt implements formatted I/O with functions analogous to C's printf and scanf.
	"log"        // Package log provides a simple logging package.
	"net"        // Package net provides a portable interface for network I/O, including TCP/IP, UDP, domain name resolution, and Unix domain sockets.
	"time"       // Package time provides functionality for measuring and displaying time.
)

// respawnLock is a semaphore-like channel used to control the spawning of dial workers.
// It ensures that only one dial worker is active at any given time.
var respawnLock chan int

// Dial establishes a TLS connection to a specified address.
// It returns two channels (sendChan and receiveChan) for data communication and an error, if any.
// This function also initiates a goroutine to manage dial worker tasks.
func Dial(addr string) (sendChan chan NetChanType, receiveChan chan NetChanType, err error) {
	// sendChan is a buffered channel for sending data of type NetChanType.
	sendChan = make(chan NetChanType, 100000)
	// receiveChan is a buffered channel for receiving data of type NetChanType.
	receiveChan = make(chan NetChanType, 100000)

	// Initialize respawnLock to control dial worker spawning.
	respawnLock = make(chan int, 1)

	// Goroutine for continuously spawning dial workers to handle network tasks.
	// It limits the number of active workers to 1 to avoid overloading.
	go func() {
		for {
			respawnLock <- 1            // Occupy the semaphore, blocking further spawns.
			time.Sleep(1 * time.Second) // Wait for 1 second before next iteration.
			go dialWorkerRun(len(respawnLock), addr, sendChan, receiveChan)
		}
	}()
	return
}

// cleanupConnection closes the given network connection and logs any errors.
func cleanupConnection(connection net.Conn) {
	// Only attempt to close the connection if it's not nil.
	if connection != nil {
		err := connection.Close()
		if err != nil {
			// Log errors encountered during connection closure.
			log.Println("Error closing dial connection:", err)
		}
	}
}

// dialWorkerRun manages a single worker for dialing and network communications.
// It handles tasks like connection establishment, data transmission, and error management.
func dialWorkerRun(workerId int, addr string, sendChan chan NetChanType, receiveChan chan NetChanType) {
	// Release a token from respawnLock upon exiting this function.
	defer func() { <-respawnLock }()

	// Generate TLS configuration for secure connections.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		// Log and return on TLS configuration errors.
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
	}

	// Attempt to establish a TLS connection to the specified address.
	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 15}
	connection, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		// Log and return if the connection fails.
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		return
	} else {
		// Log successful connection establishment.
		Println(fmt.Sprintf("Dial worker #%d connected to destination %s", workerId, addr))
	}
	// Ensure the connection is closed when the function exits.
	defer cleanupConnection(connection)

	// connectionErrorChannel communicates any errors encountered during connection.
	connectionErrorChannel := make(chan error)

	// Goroutine for reading data from the connection and handling errors.
	go func() {
		// Buffer to store data read from the connection.
		buffer := make([]byte, 1024)
		for {
			// Read data into the buffer.
			numberOfLines, err := connection.Read(buffer)
			if err != nil {
				// Send encountered errors to the connectionErrorChannel.
				connectionErrorChannel <- err
				return
			}
			// Log unexpected data received.
			if numberOfLines > 0 {
				log.Printf("Dial worker received unexpected data back: %s", buffer[:numberOfLines])
			}
		}
	}()

	// Main loop to handle outgoing data and network errors.
	for {
		select {
		case currentsendChan := <-sendChan:
			// Attempt to send data over the network, re-queue on error.
			_, networkSendingError := fmt.Fprintf(connection, "%s, %s, %s\n", currentsendChan.Id, currentsendChan.Secret, currentsendChan.Data)
			if err != nil {
				// Re-queue the data and log the error if sending fails.
				sendChan <- currentsendChan
				log.Printf("Dial worker %d exited due to sending error: %s\n", workerId, networkSendingError)
				return
			}
		case networkError := <-connectionErrorChannel:
			// Handle network errors and terminate the goroutine.
			log.Printf("Dial worker %d exited due to connection error: %s\n", workerId, networkError)
			return
		}
	}
}
