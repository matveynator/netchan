package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

// accessLock is a channel used to control access to address book map (one at a time).
var accessLock chan int

// Map for fast searching of connected client addresses and their send channels.
var addressBookMap map[string]addressBook

// Coordinator handles all addressBookMap operations.
func addressBookManager(operation string, clientAddress string, clientSendChannel chan Message) chan Message {

	// Lock access to address book
	accessLock <- 1

	defer func() {
		// Unlock access to address book
		<-accessLock
	}()

	switch operation {
	case "add":
		// Adding connected client to the address book.
		addressBookMap[clientAddress] = addressBook{Send: clientSendChannel}
		return nil
	case "delete":
		// Removing disconnected client from the address book.
		delete(addressBookMap, clientAddress)
		return nil
	case "get":
		// Return client send channel
		addressbook, ok := addressBookMap[clientAddress]
		if ok {
			return addressbook.Send
		} else {
			// If recipient not found, return nil.
			return nil
		}
	}
	return nil
}

// AdvancedListen sets up a secure TCP listener using TLS.
// It returns two channels for sending and receiving messages in special netchan type, along with an error.
// addr: The network address to listen on.
func AdvancedListen(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
	// Create channels with 100000 messages queue length.
	sendChan = make(chan Message, 100000)
	receiveChan = make(chan Message, 100000)

	// Map for fast searching of connected client addresses and their send channels.
	addressBookMap = make(map[string]addressBook)

	// access lock to address book:
	accessLock = make(chan int, 1)

	// Generate TLS configuration for secure communication.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return
	}

	// Goroutine to handle incoming connections from clients and message routing.
	go func() {
		for {
			listener, err := tls.Listen("tcp", addr, tlsConfig)
			if err != nil {
				Printonce(fmt.Sprintf("TLS listen error: %s", err))
				// Retry to listen in 5 seconds interval.
				time.Sleep(time.Second * 5)
				continue
			}
			defer listener.Close()

			go func() {
				for {
					select {
					case message := <-sendChan:
						// Forwarding messages to the appropriate recipient.
						clientReceiveChannel := addressBookManager("get", message.To, nil)
						if clientReceiveChannel == nil {
							// If recipient not found, return message to sender.
							log.Printf("Address %s not found in addressbook, returning message back sender via RECEIVE channel.", message.To)
							receiveChan <- message
						} else {
							clientReceiveChannel <- message
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
						addressBookManager("delete", address, nil)
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

				sendToClientChan := make(chan Message, 100000)
				clientAddress := conn.RemoteAddr().String()

				// Registering new client in the address book with channels that we can connect them through.
				addressBookManager("add", clientAddress, sendToClientChan)

				// Handle individual client connection.
				go handleConnection(conn, sendToClientChan, receiveChan, clientDisconnectNotifyChan)
			}
		}
	}()

	return sendChan, receiveChan, nil
}

// Listen sets up a dispatcher for handling messages between clients and the server.
// It returns two channels for sending and receiving any data types, along with an error.
// address: The network address on which the server will listen.
func Listen(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {
	// Create channels with 100000 messages queue length.
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
