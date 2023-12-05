// Package netchan provides network channel functionality for secure,
// sequenced communication between client and server. It implements a simple
// method for establishing TLS-secured TCP connections and facilitating
// message passing between client and server.
package netchan

import (
	"crypto/tls" // Package tls provides implementations of the TLS network security protocol.
	"fmt"        // Package fmt implements formatted I/O with functions analogous to C's printf and scanf.
	"log"        // Package log provides a simple logging package.
	"net"        // Package net provides a portable interface for network I/O, including TCP/IP, UDP, domain name resolution, and Unix domain sockets.
	"time"       // Package time provides functionality for measuring and displaying time.
)

// dialWorkersMaxCount is a constant that defines the maximum number of dial workers
// that can operate concurrently. The value is set to 1 to ensure sequential
// processing of tasks and ordered delivery to another server.
var dialWorkersMaxCount int = 1

// respawnLock is a channel acting as a semaphore to manage the spawning of dial workers.
// This channel limits the number of concurrently active workers to the value specified
// by dialWorkersMaxCount.
var respawnLock chan int

// Dial establishes a TLS connection to the specified address. It returns two channels
// for sending and receiving data of type NetChanType, and an error if the connection
// fails. This function also initiates a goroutine to manage dial worker tasks.
func Dial(addr string) (sendChan chan NetChanType, receiveChan chan NetChanType, err error) {
	// sendChan is a buffered channel used for sending data.
	sendChan = make(chan NetChanType, 100000)
	// receiveChan is a buffered channel used for receiving data.
	receiveChan = make(chan NetChanType, 100000)

	// respawnLock is initialized as a buffered channel to control the spawning
	// of dial worker tasks.
	respawnLock = make(chan int, dialWorkersMaxCount)

	// This goroutine continuously spawns dial workers to handle network tasks.
	// It ensures that the number of active workers does not exceed dialWorkersMaxCount.
	go func() {
		for {
			respawnLock <- 1            // Send a token to respawnLock, blocking if full.
			time.Sleep(1 * time.Second) // Pause for 1 second before next iteration.
			go dialWorkerRun(len(respawnLock), addr, sendChan, receiveChan)
		}
	}()
	return
}

// cleanupConnection is a helper function to close the given network connection.
// It logs any errors encountered during the closing process.
func cleanupConnection(connection net.Conn) {
	// Checks if the connection is non-nil before attempting to close.
	if connection != nil {
		err := connection.Close()
		if err != nil {
			log.Println("Error closing dial connection:", err)
		}
	}
}

// dialWorkerRun is a goroutine that manages a single worker for dialing and
// handling network communications. It handles connection establishment, data
// transmission, and error management.
func dialWorkerRun(workerId int, addr string, sendChan chan NetChanType, receiveChan chan NetChanType) {
	// defer statement to release a token from respawnLock when the function exits.
	defer func() { <-respawnLock }()

	// tlsConfig stores the TLS configuration used for secure connections.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
	}

	// Establish a TLS connection with the specified address.
	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 15}
	connection, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		return
	} else {
		Println(fmt.Sprintf("Dial worker #%d connected to destination %s", workerId, addr))
	}
	defer cleanupConnection(connection) // Ensure connection closure on function exit.

	// connectionErrorChannel is used to monitor and handle connection errors.
	connectionErrorChannel := make(chan error)

	// Goroutine to read data from the connection and manage errors.
	go func() {
		buffer := make([]byte, 1024) // buffer stores the data read from the connection.
		for {
			numberOfLines, err := connection.Read(buffer)
			if err != nil {
				connectionErrorChannel <- err
				return
			}
			if numberOfLines > 0 {
				log.Printf("Dial worker received unexpected data back: %s", buffer[:numberOfLines])
			}
		}
	}()

	// Main loop handling outgoing data and network errors.
	for {
		select {
		case currentsendChan := <-sendChan:
			// Sends data over the network, re-queues in case of sending error.
			_, networkSendingError := fmt.Fprintf(connection, "%s, %s, %s\n", currentsendChan.Id, currentsendChan.Secret, currentsendChan.Data)
			if err != nil {
				sendChan <- currentsendChan
				log.Printf("Dial worker %d exited due to sending error: %s\n", workerId, networkSendingError)
				return
			}
		case networkError := <-connectionErrorChannel:
			// Handles network errors and terminates the goroutine.
			log.Printf("Dial worker %d exited due to connection error: %s\n", workerId, networkError)
			return
		}
	}
}
