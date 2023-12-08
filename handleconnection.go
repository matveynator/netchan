package netchan

import (
	//	"io"
	"encoding/gob"
	"log"
	"net"
	"time"
)

// handleConnection deals with incoming messages on a network connection.
func handleConnection(conn net.Conn, send chan NetChanType, receive chan NetChanType) {

	defer conn.Close() // Ensures the connection is closed to prevent resource leaks.

	// connectionErrorChannel communicates any errors encountered during connection.
	connectionErrorChannel := make(chan error, 1000)

	// Создаем gob.Decoder и gob.Encoder с соединением
	decoder := gob.NewDecoder(conn)
	encoder := gob.NewEncoder(conn)

	// Goroutine для чтения данных из соединения
	go func() {
		for {
			var netChanMsg NetChanType
			// Декодируем данные в объект NetChanType
			err := decoder.Decode(&netChanMsg)
			if err != nil {
				connectionErrorChannel <- err
				log.Printf("Error while decoding: %s", err)
				return
			}
			// Отправляем декодированные данные в канал receive
			receive <- netChanMsg
		}
	}()

	// Main loop to handle outgoing data and network errors.
	for {
		select {

		case message, ok := <-receive:
			if !ok {
				log.Println("Exiting due to RECEIVE channel closed.")
				return
			}
			log.Printf("RECEIVED message via channel: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		case message, ok := <-send:
			if !ok {
				log.Println("Exiting due to SEND channel closed.")
				return
			}
			// Используем encoder.Encode для отправки данных
			sendingErr := encoder.Encode(message)
			if sendingErr != nil {
				// Повторно помещаем данные в очередь и регистрируем ошибку, если отправка не удалась
				send <- message
				log.Printf("Re-queue sending data as sending failed with error: %s\n", sendingErr)
			}
			log.Printf("SENT message via channel: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		case networkError := <-connectionErrorChannel:
			// Handle network errors and terminate the goroutine.
			log.Printf("Listen worker exited due to connection error: %s\n", networkError)
			return

		default:
			//log.Printf("handle connection sleeping...")
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}
}
