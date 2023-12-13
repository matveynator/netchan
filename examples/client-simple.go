// Package main demonstrates the use of the netchan package for creating a simple
// client-server application. This application includes a server and a client, where
// the client sends random messages to the server, and the server echoes them back.
package main

import (
  "github.com/matveynator/netchan" // netchan package for network channel functionalities.
  "log"                            // log package for logging messages to the console.
  "math/rand"                      // rand package for generating random data.
  "time"                           // time package for handling time-related functionality.
)

// main is the entry point of the application. It concurrently starts the server
// and the client as separate goroutines, allowing them to operate simultaneously.
func main() {
  go server() // Launch server as a goroutine.

  go client() // Launch client as a goroutine.
  go client() // Launch client as a goroutine.

  for {
    time.Sleep(1 * time.Second)
  }
}

// server function manages the server-side operations of the application.
// It continuously listens for incoming messages and sends back echo responses.
func server() {
  send, receive, err := netchan.Listen("127.0.0.1:9999")
  if err != nil {
    log.Println(err) // If an error occurs, log it and terminate the application.
    return
  }

  for {
    select {
    case message := <-receive:
      log.Printf("Server received: %v\n", message)
      // Resending received message to any ready client.
      send <- message 
    }
  }
}

func client() {
  send, receive, err := netchan.Dial("127.0.0.1:9999")
  if err != nil {
    log.Println(err) // Log the error but do not terminate; the server might still be starting.
  }

  go func() {
    for {
      randomString := randomString()
      send <- randomString
      log.Printf("Client sent: %s\n", randomString )
      time.Sleep(3 * time.Second) // Pausing for 3 seconds before sending the next message.
    }
  }()

  for {
    select {
    case message := <-receive:
      log.Printf("Client received: %v\n", message)
    }
  }

}

func randomString() string {
  var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
  s := make([]rune, 20)
  for i := range s {
    s[i] = letters[rand.Intn(len(letters))] // Randomly picking a character.
  }
  return string(s) // Converting the rune slice to a string.
}
