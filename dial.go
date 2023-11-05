// Package netchan is likely intended to provide network channel utilities,
// specifically for secure communication over TLS.
package netchan

// Importing necessary packages:
import (
	"crypto/tls"  // Provides TLS encryption functionality.
	"log"         // Logging package to log messages.
)

// Dial creates a secure client connection to a TLS server.
// It takes an address string as an input and returns a receive-only channel
// of NetChan and an error.
func Dial(addr string) (chan NetChan, error) {

	// Generate a TLS configuration for the connection with secure encryption settings.
	// This function, generateTLSConfig, is assumed to be defined elsewhere.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		// If there is an error generating the TLS configuration, return the error.
		return nil, err
	}

	// Dial a TCP connection using the provided address and the generated TLS configuration.
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		// If the connection fails, return the error.
		return nil, err
	} else {

		// Creates a buffered channel of NetChan with a capacity of 100000.
		// This channel will be used to send and receive data from the TLS connection.
		netchan := make(chan NetChan, 100000)

		// Log a formatted message indicating a successful connection.
		log.Printf("netchan connected to remote %s\n", addr)

		// Handle the connection in a separate goroutine to prevent blocking.
		// The handleConnection function is assumed to be defined elsewhere and is responsible
		// for reading from and writing to the connection.
		go handleConnection(conn, netchan)

		// Return the channel for communication and nil for the error, as the connection
		// was successful.
		return netchan, nil
	}
}
