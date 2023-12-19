// Example demonstrates the use of the netchan package for creating a simple cluster.
package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	//add netchan import:
	"github.com/matveynator/netchan"
)

// respawnLock is a channel used to control the spawning of client routines.
var respawnLock chan int

// Initiate counters for benchmark:
var seconds, sent, received, failed, total, spawned int64
var sentRate, receivedRate, totalRate, perClientSentRate, perClientReceivedRate float64

func benchmark() {
	for {
		if seconds > 0 {
			sentRate = float64(atomic.LoadInt64(&sent)) / float64(seconds)
			receivedRate = float64(atomic.LoadInt64(&received)) / float64(seconds)
			totalRate = float64(atomic.LoadInt64(&total)) / float64(seconds)
			perClientSentRate = sentRate / float64(spawned)
			perClientReceivedRate = receivedRate / float64(spawned)

			failed = sent - received
			total = sent + received

			fmt.Println()
			fmt.Printf("Sent:                  %d (%d msg/sec) - %d msg/sec per client\n", atomic.LoadInt64(&sent), int64(sentRate), int64(perClientSentRate))
			fmt.Printf("Received:              %d (%d msg/sec) - %d msg/sec per client\n", atomic.LoadInt64(&received), int64(receivedRate), int64(perClientReceivedRate))
			fmt.Printf("Processed:             %d (%d msg/sec)\n", atomic.LoadInt64(&total), int64(totalRate))
			fmt.Printf("Not received:          %d msg in %d seconds\n", atomic.LoadInt64(&failed), seconds)
			fmt.Printf("Successfully spawned   %d clients\n", spawned)

		}
		seconds++
		time.Sleep(1 * time.Second)
	}
}

func main() {
	//start 1 server:
	go server()

	//start 50 clients:
	respawnLock = make(chan int, 50)
	// Launches a goroutine that periodically tries to run dialWorkerRun.
	go func() {
		for {
			respawnLock <- 1
			go client()
		}
	}()

	// Loop forever.
	select {}
}

// server function manages the server-side operations of the application.
// It sends messages (tasks) to clients and they echoe (compute) them back to server.
func server() {
	send, receive, err := netchan.Listen("127.0.0.1:9999")

	if err != nil {
		log.Println(err)
		return
	} else {

		// Goroutine that sends messages to connected clients in our cluster.
		go func() {
			// Create new message with simple number (just a simple text for our example):
			message := 123456
			for {
				// Sending message to clients (this could be anything, for example task to factorize big number):
				send <- message
				// Increment counter for benchmark:
				atomic.AddInt64(&sent, 1)
			}
		}()

		// Server's loop for handling incoming messages.
		for {
			select {
			case <-receive:
				// Receiving results from our cluster:
				// Increment counter for benchmark:
				atomic.AddInt64(&received, 1)
				if atomic.LoadInt64(&received) == 1 {
					go benchmark()
				}
			}
		}
	}
}

// client function manages the client-side operations of the application.
// It receives messages from the server and echo them back.
func client() {
	defer func() {
		atomic.AddInt64(&spawned, -1)
	}()

	send, receive, err := netchan.Dial("127.0.0.1:9999")
	if err != nil {
		log.Println(err)
		//return
	} else {
		// Increment counter for benchmark:
		atomic.AddInt64(&spawned, 1)
		// Client's loop for handling incoming messages (tasks) from server:
		for {
			select {
			case message := <-receive:
				// Echo message back to server (this could be a computed task)
				send <- message
			}
		}
	}
}
