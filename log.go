package netchan

import (
	"log"
)

type LogData struct {
	logMessage string
	canRepeat  bool
}

var LogTask chan LogData

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	LogTask = make(chan LogData)
}

func Printonce(message string) {
	data := LogData{logMessage: message, canRepeat: false}
	LogTask <- data
}

func Println(message string) {
	data := LogData{logMessage: message, canRepeat: true}
	LogTask <- data
}

func ErrorLogWorker() {
	var previousLogMessage string

	log.Println("Started netchan logging worker in background.")

	for {
		select {
		case newLogTask := <-LogTask:
			if newLogTask.canRepeat == false {
				if previousLogMessage != newLogTask.logMessage {
					log.Println(newLogTask.logMessage)
					previousLogMessage = newLogTask.logMessage
				}
			} else {
				log.Println(newLogTask.logMessage)
				previousLogMessage = newLogTask.logMessage
			}
		}
	}
}

func init() {
	go ErrorLogWorker()
}
