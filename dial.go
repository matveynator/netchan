package netchan

import (
  "crypto/tls"
  "fmt"
  "log"
  "net"
  "time"
)

var respawnLock chan int

func NetChanDial(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
  sendChan = make(chan Message, 10000)
  receiveChan = make(chan Message, 10000)
  respawnLock = make(chan int, 1)

  go func() {
    for {
      respawnLock <- 1
      time.Sleep(1 * time.Second)
      go dialWorkerRun(addr, sendChan, receiveChan)
    }
  }()
  return
}

func dialWorkerRun(addr string, sendChan chan Message, receiveChan chan Message) {
  defer func() {
    <-respawnLock
  }()

  tlsConfig, err := generateTLSConfig()
  if err != nil {
    Printonce(fmt.Sprintf("TLS configuration error: %s", err))
    return
  }

  clientDisconnectNotifyChan := make(chan string, 100)

  log.Println("Attempting to connect to server:", addr)
  dialer := net.Dialer{Timeout: time.Second * 15}
  conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
  if err != nil {
    Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
    return
  } else {
    defer func() {
      if conn != nil {
        err := conn.Close()
        if err != nil {
          log.Println("Error closing dial connection:", err)
        }
      }
    }()

    go func() {
      for {
        select {
        case address := <- clientDisconnectNotifyChan:
          if address == conn.RemoteAddr().String() {
            err := conn.Close()
            if err != nil {
              log.Println("Dial allready closed connection to %s.", address)
            } else {
              log.Println("DIAL closed connection to %s.", address)
            }
          }
        }
      }
    }()

    log.Printf("Dial worker connected to destination %s", addr)
    handleConnection(conn, sendChan, receiveChan, clientDisconnectNotifyChan)
  }
}


func Dial(address string) (dispatcherSend chan interface{}, dispatcherReceive chan interface{}, err error) {

  dispatcherSend = make(chan interface{}, 10000)
  dispatcherReceive = make(chan interface{}, 10000)

  // Creating a network channel to send messages to the server.
  send, receive, err := NetChanDial(address)
  if err != nil {
    log.Println(err) // Log the error but do not terminate; the server might still be starting.
  } else {

    go func() {
      for {
        select {
        case payload:= <-dispatcherSend:
          data := Message{}
          data.Payload = payload
          data.To = address
          send <- data // Sending the constructed message to the server.
        }
      }
    }()

    go func() {

      readyToReceive := Message{}
      readyToReceive.To = address

      send <- readyToReceive


      for {
        select {
        case data := <-receive:
          send <- readyToReceive
          dispatcherReceive <- data.Payload // Sending the constructed message to the client.
        }
      }
    }()
  }
  return
}
