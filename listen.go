package netchan

import (
  "crypto/tls"
  "fmt"
  "log"
  "time"
)

func Listen(addr string) (sendChan chan Message, receiveChan chan Message, err error) {

  sendChan = make(chan Message, 100000)
  receiveChan = make(chan Message, 100000)

  type adressBook struct {
    Send    chan Message
  }

  addressBookMap := make(map[string]adressBook)

  tlsConfig, err := generateTLSConfig()
  if err != nil {
    return
  }

  go func() {
    for {
      listener, err := tls.Listen("tcp", addr, tlsConfig)
      if err != nil {
        Printonce(fmt.Sprintf("TLS listen error: %s", err))
        time.Sleep(time.Second * 5)
        continue
      } else {
        defer listener.Close()

        go func() {
          for {
            select {
            case message := <- sendChan:
              if adressbook, ok := addressBookMap[message.To]; ok {
                //пересылаем сообщение аддресату
                adressbook.Send <- message
              } else {
                log.Printf("Address %s not found in addressbook, returning message back sender via RECEIVE channel.", message.To)
                receiveChan <- message
              }
            }
          }
        }()

        log.Printf("Listening on %s\n", addr)

        clientDisconnectNotifyChan := make(chan string, 100000)

        go func() {
          for {
            select {
            case address := <- clientDisconnectNotifyChan:
              delete(addressBookMap, address)
              log.Printf("Connection closed and removed from address book: %s", address)
            }
          }
        }()

        for {
          conn, err := listener.Accept()
          if err != nil {
            log.Printf("Failed to accept connection: %v", err)
            continue
          } else {
            sendToClient := make(chan Message, 100000)
            clientAddress := conn.RemoteAddr().String()

            // Добавление новой записи в карту
            addressBookMap[clientAddress] = adressBook{Send: sendToClient}

            go handleConnection(conn, sendToClient, receiveChan, clientDisconnectNotifyChan)
          }
        }
      }
    }
  }()

  return sendChan, receiveChan, nil
}

