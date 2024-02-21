// Client example demonstrates the use of the netchan package for creating a simple cluster. Starts 50 clients and connects to remote server at port 9999
package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"syscall"
	"time"

	//add netchan import:
	"github.com/matveynator/netchan"
)

var maxClients uint64

func increaseMaxOpenFiles(additional uint64) error {
	var rLimit syscall.Rlimit

	// Get the current limit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return fmt.Errorf("failed to get current limit: %v", err)
	}

	// Increase the limit
	newLimit := rLimit.Cur + (additional * 2)
	if newLimit > rLimit.Max {
		newLimit = rLimit.Max
	}

	rLimit.Cur = newLimit

	// Set the new limit
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return fmt.Errorf("failed to set new limit: %v", err)
	}

	return nil
}

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
			fmt.Printf("Successfully connected %d clients\n", spawned)

		}
		seconds++
		time.Sleep(1 * time.Second)
	}
}

func main() {

	maxClients = 50

	err := increaseMaxOpenFiles(maxClients)
	if err != nil {
		fmt.Printf("Ulimit set error: %v\n", err)
	} else {
		fmt.Println("Ulimit set succesfully.")
	}

	//start maxClients clients:
	respawnLock = make(chan int, maxClients)
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

// client function manages the client-side operations of the application.
// It receives messages from the server and echo them back.
func client() {
	defer func() {
		atomic.AddInt64(&spawned, -1)
	}()

	send, receive, err := netchan.Dial("locahost:9999")
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
