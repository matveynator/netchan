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

		case currentsendChan := <-send:
			// Используем encoder.Encode для отправки данных
			if err := encoder.Encode(currentsendChan); err != nil {
				// Повторно помещаем данные в очередь и регистрируем ошибку, если отправка не удалась
				send <- currentsendChan
				log.Printf("Listen worker exited due to sending error: %s\n", err)
				return
			}

		case networkError := <-connectionErrorChannel:
			// Handle network errors and terminate the goroutine.
			log.Printf("Listen worker exited due to connection error: %s\n", networkError)
			return
		case message, ok := <-receive:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Listen received message via channel: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		default:
			//log.Printf("handle connection sleeping...")
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}
}
