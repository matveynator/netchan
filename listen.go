package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"time"
)

func Listen(addr string) (sendChan chan NetChanType, receiveChan chan NetChanType, err error) {
	sendChan = make(chan NetChanType, 100000)
	receiveChan = make(chan NetChanType, 100000)

	tlsConfig, err := generateTLSConfig()
	if err != nil {
		return
	}

	// Запускаем горутину для прослушивания и обработки соединений
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

				go handleConnection(conn, sendChan, receiveChan)
			}
		}
	}()

	// Возвращаем каналы, ошибка уже обработана выше
	return sendChan, receiveChan, nil
}

