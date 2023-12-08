// Package netchan provides network channel functionality for secure,
// sequenced communication between client and server. It implements a simple
// method for establishing TLS-secured TCP connections and facilitating
// message passing between communicating peers.
package netchan

import (
  "crypto/tls"
  "fmt"
  "log"
	"net"
  "time"
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

	// Initialize non-buffered (=1) respawnLock to control dial worker spawning.
	respawnLock = make(chan int, 1)

	// Goroutine for continuously spawning dial workers to handle network tasks.
	// It limits the number of active workers to 1 to avoid overloading.
	go func() {
		dialerId := 1
		for {
			respawnLock <- 1            // Occupy the semaphore, blocking further spawns.
			time.Sleep(1 * time.Second) // Wait for 1 second before next iteration.
			go dialWorkerRun(dialerId, addr, sendChan, receiveChan)
			dialerId++
		}
	}()
	return
}

// dialWorkerRun manages a single worker for dialing and network communications.
// It handles tasks like connection establishment, data transmission, and error management.
// dialWorkerRun manages a single worker for dialing and network communications.
func dialWorkerRun(dialerId int, addr string, sendChan chan NetChanType, receiveChan chan NetChanType) {
	defer func() {
		// Release a token from respawnLock upon exiting this function.
		<-respawnLock
	}()

	tlsConfig, err := generateTLSConfig()
	if err != nil {
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
	}

	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 15}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		return
	}
	defer func() {
		if conn != nil {
			err := conn.Close()
			if err != nil {
				log.Println("Error closing dial connection:", err)
			}
		}
	}()

	log.Printf("Dial worker #%d connected to destination %s", dialerId, addr)

	// Use handleConnection to manage the established connection.
	handleConnection(conn, sendChan, receiveChan)

}
