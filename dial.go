package netchan

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"
)

var respawnLock chan int

func Dial(addr string) (sendChan chan Message, receiveChan chan Message, err error) {
	sendChan = make(chan Message, 100000)
	receiveChan = make(chan Message, 100000)
	respawnLock = make(chan int, 1)

	go func() {
		dialerId := 1
		for {
			respawnLock <- 1
			time.Sleep(1 * time.Second)
			go dialWorkerRun(dialerId, addr, sendChan, receiveChan)
			dialerId++
		}
	}()
	return
}

func dialWorkerRun(dialerId int, addr string, sendChan chan Message, receiveChan chan Message) {
	defer func() {
		<-respawnLock
	}()

	tlsConfig, err := generateTLSConfig()
	if err != nil {
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
	}

	log.Println("Attempting to connect to server:", addr)
	dialer := net.Dialer{Timeout: time.Second * 15}
	conn, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)
	if err != nil {
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s", addr, err))
		return
	}
	defer func() {
		if conn != nil {
			err := conn.Close()
			if err != nil {
				log.Println("Error closing dial connection:", err)
			}
		}
	}()

	log.Printf("Dial worker #%d connected to destination %s", dialerId, addr)
	handleConnection(conn, sendChan, receiveChan)

}
