package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

func NetChanListen(addr string) (sendChan chan Message, receiveChan chan Message, err error) {

	sendChan = make(chan Message, 10000)
	receiveChan = make(chan Message, 10000)

	type addressBook struct {
		Send chan Message
	}

	addressBookMap := make(map[string]addressBook)

	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return
	}

	go func() {
		for {
			listener, err := tls.Listen("tcp", addr, tlsConfig)
			if err != nil {
				Printonce(fmt.Sprintf("TLS listen error: %s", err))
				time.Sleep(time.Second * 5)
				continue
			} else {
				defer listener.Close()

				go func() {
					for {
						select {
						case message := <-sendChan:
							if adressbook, ok := addressBookMap[message.To]; ok {
								//пересылаем сообщение адресату
								adressbook.Send <- message
							} else {
								log.Printf("Address %s not found in addressbook, returning message back sender via RECEIVE channel.", message.To)
								receiveChan <- message
							}
						}
					}
				}()

				log.Printf("Listening on %s\n", addr)

				clientDisconnectNotifyChan := make(chan string, 10000)

				go func() {
					for {
						select {
						case address := <-clientDisconnectNotifyChan:
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
					} else {
						sendToClient := make(chan Message, 10000)
						clientAddress := conn.RemoteAddr().String()

						// Добавление новой записи в карту
						addressBookMap[clientAddress] = addressBook{Send: sendToClient}

						go handleConnection(conn, sendToClient, receiveChan, clientDisconnectNotifyChan)
					}
				}
			}
		}
	}()

	return sendChan, receiveChan, nil
}

// server function manages the server-side operations of the application.
// It continuously listens for incoming messages and sends back echo responses.
func Listen(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {

	dispatcherSend = make(chan interface{}, 10000)
	dispatcherReceive = make(chan interface{}, 10000)

	// Объявляем канал хранения адресов клиентов которые готовы принять задачу/сообщение
	var readyClientsAddressList = make(chan string, 10000)

	// Establishing a network channel to receive and send messages.
	// This channel will be used for communication with the clients.
	send, receive, err := NetChanListen(address)
	if err != nil {
		log.Fatal(err) // If an error occurs, log it and terminate the application.
		return
	} else {
		//sending messages to clients who are ready to receive them (from the list of ready clients one per message)
		go func() {
			for {
				select {
				case payload := <-dispatcherSend:
					data := Message{}
					data.Payload = payload
					data.To = <-readyClientsAddressList
					send <- data // Sending the constructed message to client who is ready to receive connection.
				}
			}
		}()

		go func() {
			for {
				select {
				case data := <-receive:
					if data.Payload == nil {
						readyClientsAddressList <- data.From
					} else {
						dispatcherReceive <- data.Payload // Sending the simple message to the server from client.
					}
				}
			}
		}()
	}
	return
}
