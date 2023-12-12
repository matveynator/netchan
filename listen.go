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
			}

			defer listener.Close()
			log.Printf("Listening on %s\n", addr)

			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Printf("Failed to accept connection: %v", err)
					continue
				}
				//сообщаяем адрес с которого подключился клиент в канал iceChan
				go handleConnection(conn, sendChan, receiveChan)
			}
		}
	}()

	return sendChan, receiveChan, nil
}
