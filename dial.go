// Package netchan provides a network communication framework using channels.
package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"
)

// AdvancedDial establishes a secure TLS connection to the given address.
// It returns two channels for sending and receiving Message structs,
// and an error if the initial connection setup fails.
func AdvancedDial(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
	sendChan = make(chan Message, 1)
	receiveChan = make(chan Message, 1000)

	// A channel to signal successful connection.
	connected := make(chan bool)

	// Keep one reconnect worker at a time.
	respawnLock := make(chan struct{}, 1)

	// Launch a goroutine that periodically tries to reconnect.
	go func() {
		for {
			respawnLock <- struct{}{}
			time.Sleep(1 * time.Second)
			go dialWorkerRun(addr, sendChan, receiveChan, connected, respawnLock)
		}
	}()

	// Wait for a successful connection signal.
	<-connected
	return sendChan, receiveChan, nil
}

// dialWorkerRun handles the actual connection setup and messaging for AdvancedDial.
// It manages the TLS connection and forwards messages between the client and server.
func dialWorkerRun(addr string, sendChan chan Message, receiveChan chan Message, connected chan bool, respawnLock chan struct{}) {
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
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		return
	}
	defer func() {
		if conn == nil {
			return
		}
		if closeErr := conn.Close(); closeErr != nil {
			log.Println("Error closing dial connection:", closeErr)
		}
	}()

	// Handle connection closure if the server disconnects.
	go func() {
		for address := range clientDisconnectNotifyChan {
			if address != conn.RemoteAddr().String() {
				continue
			}
			if closeErr := conn.Close(); closeErr != nil {
				log.Printf("DIAL already closed connection to %s.", address)
			} else {
				log.Printf("DIAL closed connection to %s.", address)
			}
		}
	}()

	// If connection is successful, send a signal.
	connected <- true
	log.Printf("Dial worker connected to destination %s", addr)

	handleConnection(conn, sendChan, receiveChan, clientDisconnectNotifyChan)
}

// Dial creates channels for sending and receiving data to a specified address.
// It uses AdvancedDial to establish a network connection and then sets up
// channels to send and receive data.
func Dial(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {
	dispatcherSend = make(chan interface{}, 1)
	dispatcherReceive = make(chan interface{}, 1000)

	// Establish a TLS connection to the server.
	send, receive, err := AdvancedDial(address)
	if err != nil {
		return nil, nil, err
	}

	// Handle sending messages to the server.
	go func() {
		for payload := range dispatcherSend {
			message := Message{Payload: payload, To: address}
			send <- message
		}
	}()

	// Handle receiving messages from server.
	go func() {
		// Send empty message to server to notify that we are ready to receive messages.
		readyToReceive := Message{To: address}
		send <- readyToReceive

		for data := range receive {
			dispatcherReceive <- data.Payload
		}
	}()

	return dispatcherSend, dispatcherReceive, nil
}
