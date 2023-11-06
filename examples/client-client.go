package main

import (
	"log"
	"test/netchan"
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
	ncs := make(chan netchan.NetChan, 100000) // Channel for NetChan instances.

	err := netchan.ListenAndServe("127.0.0.1:9999", ncs)
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case message, ok := <-ncs:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		default:
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}

}

func client() {

	ncc := make(chan netchan.NetChan, 100000) // Channel for NetChan instances.
	// Подключаемся к серверу
	err := netchan.Dial("127.0.0.1:9999", ncc)
	if err != nil {
		log.Fatal(err)
	} else {
		log.Println("connected")
	}

	data := netchan.NetChan{ // Assuming NetChan is the correct exported type
		Id:     "1",
		Secret: "strongpass",
		Data:   "Привет, мир!",
	}

	ncc <- data

	for {
		select {
		case message, ok := <-ncc:
			if !ok {
				log.Println("Main netchan channel closed, exiting")
				return
			}
			log.Printf("Received: ID=%s, Secret=%s, Data=%s\n", message.Id, message.Secret, message.Data)

		default:
			time.Sleep(time.Second) // Throttles the loop to avoid high CPU usage.
		}
	}

}
