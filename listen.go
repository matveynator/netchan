package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

// Listen initializes a TLS listener on a specified address. It facilitates the creation
// of two channels for bi-directional communication (sending and receiving) and handles
// incoming connections securely over TLS.
//
// Parameters:
// - addr: The network address on which the function listens for incoming TLS connections.
//
// Returns:
// - sendChan: A channel of type NetChanType for sending data.
// - receiveChan: A channel of type NetChanType for receiving data.
// - err: An error that may occur during the setup of the listener. If setup is successful, err is nil.
func Listen(addr string) (sendChan chan NetChanType, receiveChan chan NetChanType, err error) {
	// sendChan: A buffered channel for outgoing data, capable of holding 100000 items.
	// It's used to send data to connected clients.
	sendChan = make(chan NetChanType, 100000)

	// receiveChan: A buffered channel for incoming data, also capable of holding 100000 items.
	// It's used to receive data from connected clients.
	receiveChan = make(chan NetChanType, 100000)

	// tlsConfig: A configuration object for setting up a TLS listener.
	// err: Captures any errors encountered during the generation of the TLS configuration.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		// If there is an error in generating TLS configuration, the function returns early.
		return
	}

	// Infinite loop to continuously attempt to establish a TLS listener.
	// This loop ensures the service keeps trying to listen even if initial attempts fail.
	for {
		// listener: Represents the TLS listener object for accepting incoming connections.
		// err: Captures any errors encountered during the establishment of the TLS listener.
		listener, err := tls.Listen("tcp", addr, tlsConfig)
		if err != nil {
			// If establishing the listener fails, log the error, wait for 5 seconds, and retry.
			Printonce(fmt.Sprintf("TLS listen error: %s", err))
			time.Sleep(time.Second * 5)
			continue
		}

		// Ensure that the listener is closed when the function exits,
		// to release resources and prevent memory leaks.
		defer listener.Close()

		// Logging the address to indicate that the service is actively listening on it.
		log.Printf("Listening on %s\n", addr)

		// Infinite loop to continuously accept and process incoming connections.
		// This allows the service to handle multiple connections over its lifetime.
		for {
			// conn: Represents a single accepted connection from a client.
			// err: Captures any errors encountered during the connection acceptance.
			conn, err := listener.Accept()
			if err != nil {
				// If accepting a connection fails, log the error and continue to
				// the next iteration to accept more connections.
				log.Printf("Failed to accept connection: %v", err)
				continue
			}

			// Launch a new goroutine for handling each connection.
			// This enables concurrent processing of multiple connections.
			go handleConnection(conn, sendChan, receiveChan)
		}
	}
}
