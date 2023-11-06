package netchan

import (
	"log"
)

type LogData struct {
	logMessage string
	canRepeat bool
	//processId string
}


var LogTask chan LogData

func init () {
	//print microseconds
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	//initialize buffered log channels:
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
			//в случае если есть задание в канале LogTask
		case newLogTask := <- LogTask :
			//log.Println("newLogTask received")
			if newLogTask.canRepeat==false {
				//не повторяем ошибки бесконечно:
				if previousLogMessage != newLogTask.logMessage  {
					log.Println(newLogTask.logMessage)
					previousLogMessage = newLogTask.logMessage
				}
			}	else {
				log.Println(newLogTask.logMessage)
				previousLogMessage = newLogTask.logMessage
			}
		}
	}
}

func init() {
	go	ErrorLogWorker()
}

