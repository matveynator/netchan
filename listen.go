package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

// addressBookRequest describes a single address book operation.
// A dedicated manager goroutine owns the map so concurrent access stays channel-driven.
type addressBookRequest struct {
	operation   string
	address     string
	sendChannel chan Message
	response    chan chan Message
}

// startAddressBookManager starts the single owner of the connection address book.
func startAddressBookManager() chan<- addressBookRequest {
	requests := make(chan addressBookRequest)
	go func() {
		addressBookMap := make(map[string]addressBook)
		for request := range requests {
			switch request.operation {
			case "add":
				addressBookMap[request.address] = addressBook{Send: request.sendChannel}
			case "delete":
				delete(addressBookMap, request.address)
			case "get":
				request.response <- addressBookMap[request.address].Send
			}
		}
	}()
	return requests
}

// addressBookGet returns a client's send channel or nil when the client is not registered.
func addressBookGet(requests chan<- addressBookRequest, address string) chan Message {
	response := make(chan chan Message, 1)
	requests <- addressBookRequest{operation: "get", address: address, response: response}
	return <-response
}

// AdvancedListen sets up a secure TCP listener using TLS.
// It returns two channels for sending and receiving messages in special netchan type, along with an error.
// addr: The network address to listen on.
func AdvancedListen(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
	sendChan = make(chan Message, 1)
	receiveChan = make(chan Message, 1000)

	// A channel to signal successful bind.
	serverBoundOnPort := make(chan bool)
	addressBookRequests := startAddressBookManager()

	// Generate TLS configuration for secure communication.
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return nil, nil, err
	}

	// Goroutine to handle incoming connections from clients and message routing.
	go func() {
		for {
			listener, listenErr := tls.Listen("tcp", addr, tlsConfig)
			if listenErr != nil {
				Printonce(fmt.Sprintf("TLS listen error: %s", listenErr))
				// Retry to listen in 5 seconds interval.
				time.Sleep(5 * time.Second)
				continue
			}

			defer listener.Close()
			serverBoundOnPort <- true

			go func() {
				for message := range sendChan {
					clientReceiveChannel := addressBookGet(addressBookRequests, message.To)
					if clientReceiveChannel == nil {
						log.Printf("Address %s not found in addressbook, returning message back sender via RECEIVE channel.", message.To)
						receiveChan <- message
						continue
					}
					clientReceiveChannel <- message
				}
			}()

			log.Printf("Listening on %s\n", addr)

			clientDisconnectNotifyChan := make(chan string, 100000)
			go func() {
				for address := range clientDisconnectNotifyChan {
					addressBookRequests <- addressBookRequest{operation: "delete", address: address}
					log.Printf("Connection closed and removed from address book: %s", address)
				}
			}()

			for {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					log.Printf("Failed to accept connection: %v", acceptErr)
					continue
				}

				sendToClientChan := make(chan Message, 1)
				clientAddress := conn.RemoteAddr().String()
				addressBookRequests <- addressBookRequest{operation: "add", address: clientAddress, sendChannel: sendToClientChan}

				// Handle individual client connection.
				go handleConnection(conn, sendToClientChan, receiveChan, clientDisconnectNotifyChan)
			}
		}
	}()

	// Wait for a successful bind signal.
	<-serverBoundOnPort
	return sendChan, receiveChan, nil
}

// Listen sets up a dispatcher for handling messages between clients and the server.
// It returns two channels for sending and receiving any data types, along with an error.
// address: The network address on which the server will listen.
func Listen(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {
	dispatcherSend = make(chan interface{}, 1)
	dispatcherReceive = make(chan interface{}, 1000)

	// Channel with addresses of clients that are ready to receive data.
	readyClientsAddressList := make(chan string, 10000000)

	send, receive, err := AdvancedListen(address)
	if err != nil {
		return nil, nil, err
	}

	// Goroutine for sending messages to ready clients.
	go func() {
		for payload := range dispatcherSend {
			message := Message{Payload: payload, To: <-readyClientsAddressList}
			send <- message
		}
	}()

	// Goroutine for handling received messages and client readiness.
	go func() {
		for data := range receive {
			readyClientsAddressList <- data.From
			if data.Payload != nil {
				dispatcherReceive <- data.Payload
			}
		}
	}()

	return dispatcherSend, dispatcherReceive, nil
}
