package netchan // Defines the package name which is netchan

import ( // Imports multiple packages into the program
	"crypto/tls"  // for implementing TLS encryption
	"crypto/x509" // for X.509 cryptography functions
	"io"          // for basic I/O primitives
	"log"         // for logging
	"net"         // for network I/O
)

// ListenAndServe starts a TLS server on the specified address.
func ListenAndServe(addr string) (<-chan netChan, error) {

	// Generate a TLS config with certificates and other settings.
	tlsConfig, err := generateTLSConfig() // This function is not shown here but is assumed to generate the TLS configuration
	if err != nil {
		return err // If there's an error, return it immediately
	}

	// Creates a buffered channel of netChan with a capacity of 100000.
	// This channel will be used to send and receive data from the TLS connection.
	netchan := make(chan netChan, 100000) // Initialize the channel to buffer connections

	for {
		// Create a listener that will listen on the specified TCP address with the TLS configuration.
		listener, err := tls.Listen("tcp", addr, tlsConfig) // 'tcp' indicates the use of the TCP/IP protocol for the listener
		defer listener.Close()                              // Ensure the listener is closed when the function exits
		if err != nil {
			log.Println(err) // Log any errors that occur when trying to listen
		} else {
			log.Printf("netchan is listening on %s\n", addr) // Log that the server is successfully listening on the address

			// Continuously accept new connections
			for {
				conn, err := listener.Accept() // Accept a new connection
				if err != nil {
					log.Printf("Failed to accept connection: %v", err) // Log any errors that occur on accepting a connection
					continue                                           // Continue to the next iteration to accept more connections
				}

				// Handle the connection in a new goroutine
				// This allows the server to handle multiple connections concurrently
				go handleConnection(conn, netchan) // This function is not shown here but is assumed to handle the connection
			}
		}
		// Wait a bit before the next attempt to bind to the port if there was an error earlier
		time.Sleep(time.Second * 5) // Sleep for 5 seconds before trying to listen again
	}
}
