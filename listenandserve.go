package netchan

import (
	"crypto/tls"
	"log"
	"time"
)

// ListenAndServe starts a TLS server on the specified address and returns a channel for communication.
func ListenAndServe(addr string, netchan chan NetChan) error {
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return err
	}

	for {
		listener, err := tls.Listen("tcp", addr, tlsConfig)
		if err != nil {
			log.Println(err)            // Log errors related to starting the listener.
			time.Sleep(time.Second * 5) // Wait before retrying.
			continue
		} else {
			defer listener.Close() // Ensure the listener is closed on function exit.

			log.Printf("Listening on %s\n", addr) // Log successful listener start.

			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Printf("Failed to accept connection: %v", err)
					continue // Continue accepting new connections.
				} else {
				go handleConnection(conn, netchan) // Handle connections in separate goroutines.
				}
			}
		}
	}
}
