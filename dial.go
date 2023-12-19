// Package netchan provides a network communication framework using channels.
package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"
)

// respawnLock is a channel used to control the spawning of dial worker routines.
var respawnLock chan int

// AdvancedDial establishes a secure TLS connection to the given address.
// It returns two channels for sending and receiving Message structs,
// and an error if the initial connection setup fails.
func AdvancedDial(addr string) (sendChan chan Message, receiveChan chan Message, err error) {

	// send channel with 1 message queue length.
	sendChan = make(chan Message, 1)
	receiveChan = make(chan Message, 1000)

	// A channel to signal successful connection
	connected := make(chan bool)

	// Spawn only one dial connection:
	respawnLock = make(chan int, 1)

	// Launches a goroutine that periodically tries to run dialWorkerRun.
	go func() {
		for {
			respawnLock <- 1
			time.Sleep(1 * time.Second)
			go dialWorkerRun(addr, sendChan, receiveChan, connected)
		}
	}()

	// Wait for a successful connection signal
	<-connected
	return
}

// dialWorkerRun handles the actual connection setup and messaging for AdvancedDial.
// It manages the TLS connection and forwards messages between the client and server.
func dialWorkerRun(addr string, sendChan chan Message, receiveChan chan Message, connected chan bool) {
	defer func() {
		<-respawnLock
	}()

	tlsConfig, err := generateTLSConfig()
	if err != nil {
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
	}

	clientDisconnectNotifyChan := make(chan string, 100)

	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 15}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		//return
	} else {
		defer func() {
			if conn != nil {
				err := conn.Close()
				if err != nil {
					log.Println("Error closing dial connection:", err)
				}
			}
		}()

		// Handles connection closure if the client disconnects.
		go func() {
			for {
				select {
				case address := <-clientDisconnectNotifyChan:
					if address == conn.RemoteAddr().String() {
						err := conn.Close()
						if err != nil {
							log.Printf("DIAL already closed connection to %s.", address)
						} else {
							log.Printf("DIAL closed connection to %s.", address)
						}
					}
				}
			}
		}()

		// If connection is successful, send a signal
		connected <- true

		log.Printf("Dial worker connected to destination %s", addr)

		handleConnection(conn, sendChan, receiveChan, clientDisconnectNotifyChan)
	}
}

// Dial creates channels for sending and receiving data to a specified address.
// It uses AdvancedDial to establish a network connection and then sets up
// channels to send and receive data.
func Dial(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {
	// send channel with 1 message queue length.
	dispatcherSend = make(chan interface{}, 1)
	dispatcherReceive = make(chan interface{}, 1000)

	// Establishes a TLS connection to the server.
	send, receive, err := AdvancedDial(address)
	if err != nil {
		log.Println(err) // Log the error but do not terminate; the server might still be starting.
	} else {
		// Handles sending messages to the server.
		go func() {
			for {
				select {
				case payload := <-dispatcherSend:
					data := Message{}
					data.Payload = payload
					data.To = address
					send <- data // Sending the constructed message to the server.
				}
			}
		}()

		// Gouroutine handles receiving messages from server.
		go func() {
			// Send empty message to server to notify that we are ready to receive messages:
			readyToReceive := Message{}
			readyToReceive.To = address
			send <- readyToReceive

			// Loop than will proxy incoming network data to client receive channel:
			for {
				select {
				case data := <-receive:
					//Sending the constructed message to the client.
					dispatcherReceive <- data.Payload
				}
			}
		}()
	}
	return
}
