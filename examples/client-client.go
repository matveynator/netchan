package main

import (
	"github.com/matveynator/netchan"
	"log"
	"time"
)

func main() {
	// Запустим сервер в горутине

	go server()
	time.Sleep(3 * time.Second)
	go client()

	for {
	}
}

func server() {
	// Слушаем входящие соединения
	send, receive, err := netchan.Listen("127.0.0.1:9999")
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case message, ok := <-receive:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Server received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)
			//send mesaage back
			time.Sleep(time.Second)
			send <- message

		default:
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}

}

func client() {

	// Подключаемся к серверу
	send, receive, err := netchan.Dial("127.0.0.1:9999")
	if err != nil {
		log.Println(err)
	}

	data := netchan.NetChanType{ // Assuming NetChan is the correct exported type
		Id:     "1",
		Secret: "strongpass",
		Data:   "Привет, мир!",
	}

	send <- data

	for {
		select {
		case message, ok := <-receive:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Client received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)
			//send mesaage back
			time.Sleep(time.Second)
			send <- message

		default:
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}

}
