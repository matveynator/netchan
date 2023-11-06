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
func Dial(addr string, netchan chan NetChan) error {

	// Obtain TLS configuration with robust security.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return err // Propagate the error if TLS configuration fails.
	}

	// Attempt to establish a TLS connection with the server.
	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 10}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		log.Printf("Failed to connect: %v\n", err)
		return err // Report failure to connect.
	} else {

		// Announce successful connection establishment.
		log.Printf("netchan connected to remote %s\n", addr)
		
		// Handle connection in a separate goroutine	
		go handleConnection(conn, netchan)
		
		//
		return nil
	}
}
