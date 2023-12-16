package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

// AdvancedListen sets up a secure TCP listener using TLS.
// It returns two channels for sending and receiving messages in special netchan type, along with an error.
// addr: The network address to listen on.
func AdvancedListen(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
	sendChan = make(chan Message, 100000)
	receiveChan = make(chan Message, 100000)

	// addressBook is a struct to hold the send channel for each connected client.
	type addressBook struct {
		Send chan Message
	}

	// Map for fast searching of connected client addresses and their send channels.
	addressBookMap := make(map[string]addressBook)

	// Generate TLS configuration for secure communication.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return
	}

	// Goroutine to handle incoming connections and message routing.
	go func() {
		for {
			listener, err := tls.Listen("tcp", addr, tlsConfig)
			if err != nil {
				Printonce(fmt.Sprintf("TLS listen error: %s", err))
				time.Sleep(time.Second * 5)
				continue
			}
			defer listener.Close()

			go func() {
				for {
					select {
					case message := <-sendChan:
						// Forwarding messages to the appropriate recipient.
						if adressbook, ok := addressBookMap[message.To]; ok {
							adressbook.Send <- message
						} else {
							// If recipient not found, return message to sender.
							log.Printf("Address %s not found in addressbook, returning message back sender via RECEIVE channel.", message.To)
							receiveChan <- message
						}
					}
				}
			}()

			log.Printf("Listening on %s\n", addr)

			clientDisconnectNotifyChan := make(chan string, 100000)

			go func() {
				for {
					select {
					case address := <-clientDisconnectNotifyChan:
						// Removing disconnected clients from the address book.
						delete(addressBookMap, address)
						log.Printf("Connection closed and removed from address book: %s", address)
					}
				}
			}()

			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Printf("Failed to accept connection: %v", err)
					continue
				}

				sendToClient := make(chan Message, 100000)
				clientAddress := conn.RemoteAddr().String()

				// Registering new client in the address book.
				addressBookMap[clientAddress] = addressBook{Send: sendToClient}

				// Handle individual client connection.
				go handleConnection(conn, sendToClient, receiveChan, clientDisconnectNotifyChan)
			}
		}
	}()

	return sendChan, receiveChan, nil
}

// Listen sets up a dispatcher for handling messages between clients and the server.
// It returns two channels for sending and receiving any data types, along with an error.
// address: The network address on which the server will listen.
func Listen(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {
	dispatcherSend = make(chan interface{}, 100000)
	dispatcherReceive = make(chan interface{}, 100000)

	// Channel which holds addresses of clients that are ready to receive data.
	var ReadyClientsAddressList = make(chan string, 1000000)

	send, receive, err := AdvancedListen(address)
	if err != nil {
		log.Fatal(err)
		return
	}

	// Goroutine for dispatching messages to ready clients.
	go func() {
		for {
			select {
			case payload := <-dispatcherSend:
				data := Message{}
				data.Payload = payload
				data.To = <-ReadyClientsAddressList
				send <- data
			}
		}
	}()

	// Goroutine for handling received messages and client readiness.
	go func() {
		for {
			select {
			case data := <-receive:
                                ReadyClientsAddressList <- data.From
                                if data.Payload != nil {
					// Passing the message payload to the server.
					dispatcherReceive <- data.Payload
				}
			}
		}
	}()

	return
}
