package netchan

import (
	"encoding/gob"
	"log"
	"net"
	"time"
)

func handleConnection(conn net.Conn, send chan NetChanType, receive chan NetChanType) {
	defer conn.Close()

	connectionErrorChannel := make(chan error, 1000)
	decoder := gob.NewDecoder(conn)
	encoder := gob.NewEncoder(conn)

	go func() {
		for {
			var netChanMsg NetChanType
			err := decoder.Decode(&netChanMsg)
			if err != nil {
				connectionErrorChannel <- err
				log.Printf("Error while decoding: %s", err)
				return
			}
			receive <- netChanMsg
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
			log.Printf("SENT message via channel: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		case networkError := <-connectionErrorChannel:
			log.Printf("Listen worker exited due to connection error: %s\n", networkError)
			return

		default:
			time.Sleep(time.Second)
		}
	}
}
