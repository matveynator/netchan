// Package netchan provides tools for establishing secure network channels.
package netchan

import (
	"crypto/tls" // For secure communication using TLS.
	"log"        // For logging information.
	"fmt"
	"net"
	"time"
)
//оставляем только один процесс который будет брать задачи и передавать их далее на другой сервер (чтобы сохранялась последовательность)
var dialWorkersMaxCount int = 1
var respawnLock chan int

func Dial(addr string) (sendChan chan NetChanType, receiveChan chan NetChanType, err error) {

  	sendChan = make(chan NetChanType, 100000)    // Channel for outgoing data.
  	receiveChan = make(chan NetChanType, 100000) // Channel for incoming data.

		log.Printf("Dialing %s\n", addr)
		
		//initialize unblocking channel to guard respawn tasks
		respawnLock = make(chan int, dialWorkersMaxCount)

		go func() {
			for {
				// will block if there is dialWorkersMaxCount ints in respawnLock
				respawnLock <- 1 
				//sleep 1 second
				time.Sleep(1 * time.Second)
				go dialWorkerRun(len(respawnLock), addr, sendChan, receiveChan)
			}
		}()
		return 
}

//close connection on programm exit
func deferCleanup(connection net.Conn) {
	<-respawnLock
	if connection != nil {
		err := connection.Close() 
		if err != nil {
			log.Println("Error closing dial connection:", err)
		}
	}
}

func dialWorkerRun(workerId int, addr string, sendChan chan NetChanType, receiveChan chan NetChanType) {

 // Obtain TLS configuration with robust security.
  tlsConfig, err := generateTLSConfig()
  if err != nil {
		Printonce(fmt.Sprintf("TLS configuration error: %s", err))
		return
  }

  // Attempt to establish a TLS connection with the server.
  log.Println("Attempting to connect to server:", addr)
  dialer := net.Dialer{Timeout: time.Second * 15}
  connection, err := tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig)

	if err != nil  {
		log.Println("1111")
		Printonce(fmt.Sprintf("Dial destination %s unreachable. Error: %s",  addr, err))
		return

	} else {
		log.Println("2222")
		Println(fmt.Sprintf("Dial worker #%d connected to destination %s", workerId, addr))
	}

	defer deferCleanup(connection)

	//initialise connection error channel
	connectionErrorChannel := make(chan error)

	go func() {
		buffer := make([]byte, 1024)
		for {
			numberOfLines, err := connection.Read(buffer)
			if err != nil {
				connectionErrorChannel <- err
				return
			}
			if numberOfLines > 0 {
				log.Printf("Dial worker received unexpected data back: %s", buffer[:numberOfLines])
			}
		}
	}()

	for {
		select {
			//в случае если есть задание в канале sendChan
		case currentsendChan := <- sendChan :
			//fmt.Println("dialWorker", workerId, "processing new job...")
			_, networkSendingError := fmt.Fprintf(connection, "%s, %s, %s\n", currentsendChan.Id, currentsendChan.Secret, currentsendChan.Data)
			if err != nil {
				//в случае потери связи во время отправки мы возвращаем задачу обратно в канал sendChan
				sendChan <- currentsendChan
				log.Printf("Dial worker %d exited due to sending error: %s\n", workerId, networkSendingError)
				//и завершаем работу гоурутины
				return
			} else {
				//fmt.Println("dialWorker", workerId, "finished job.")
			}
		case networkError := <-connectionErrorChannel :
			//обнаружена сетевая ошибка - завершаем гоурутину
			log.Printf("Dial worker %d exited due to connection error: %s\n", workerId, networkError)
			return
		}
	}
}



