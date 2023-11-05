// Package netchan provides tools for establishing secure network channels.
package netchan

import (
	"crypto/tls" // For secure communication using TLS.
	"log"        // For logging information.
	"net"
	"time"
)

// Dial establishes a secure connection to a TLS server at the given address.
// It returns a channel for NetChan instances to communicate through, and an error, if any.
func Dial(addr string) (chan NetChan, error) {

	// Obtain TLS configuration with robust security.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return nil, err // Propagate the error if TLS configuration fails.
	}

	// Attempt to establish a TLS connection with the server.
	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 10}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		log.Printf("Failed to connect: %v\n", err)
		return nil, err // Report failure to connect.
	}

	// Announce successful connection establishment.
	log.Printf("netchan connected to remote %s\n", addr)

  // Create a channel for NetChan instances with ample buffer space.
  netchan := make(chan NetChan, 100000)

	// Delegate the connection management to a concurrent routine.
	go handleConnection(conn, netchan)

	// Provide the caller with the channel for communication.
	return netchan, nil
}
