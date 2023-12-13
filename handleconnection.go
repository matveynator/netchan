package netchan

import (
	"encoding/gob"
	"log"
	"net"
	"time"
)

func handleConnection(conn net.Conn, send chan Message, receive chan Message, clientDisconnectNotifyChan chan string) {

        defer func() {
          clientDisconnectNotifyChan <- conn.RemoteAddr().String()
          conn.Close()
        }()

	connectionErrorChannel := make(chan error, 1000)

	decoder := gob.NewDecoder(conn)
	encoder := gob.NewEncoder(conn)

	go func() {
		for {
			var msg Message
			err := decoder.Decode(&msg)
			if err != nil {
				connectionErrorChannel <- err
				log.Printf("Error while decoding: %s", err)
				return
			}
			msg.From = conn.RemoteAddr().String()
			receive <- msg
		}
	}()

	for {
		select {
		case message, ok := <-send:
			if !ok {
				log.Println("Exiting due to SEND channel closed.")
				return
			}
			
			sendingErr := encoder.Encode(message)
			if sendingErr != nil {
				send <- message
				log.Printf("Re-queue sending data as sending failed with error: %s\n", sendingErr)
			}
			log.Printf("SENT message via channel: %v\n", message)

		case networkError := <-connectionErrorChannel:
			log.Printf("Netchan handle connection worker exited due to connection error: %s\n", networkError)
			return

		default:
			time.Sleep(time.Second)
		}
	}
}
