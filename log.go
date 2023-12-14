package netchan

import (
	"log"
)

// LogData is a structure that holds a log message and a flag indicating if the message can be repeated.
type LogData struct {
	logMessage string // The message to log
	canRepeat  bool   // Flag indicating if the log message can be repeated
}

// LogTask is a channel that transmits LogData instances for logging.
var LogTask chan LogData

// init sets up the logger and initializes the LogTask channel.
func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds) // Configure logger to include standard flags and microsecond precision
	LogTask = make(chan LogData)                    // Initialize the LogTask channel
}

// Printonce sends a log message to LogTask that should not be repeated.
func Printonce(message string) {
	data := LogData{logMessage: message, canRepeat: false} // Create a LogData instance with the message and no repeat
	LogTask <- data                                        // Send the LogData instance to the LogTask channel
}

// Println sends a log message to LogTask that can be repeated.
func Println(message string) {
	data := LogData{logMessage: message, canRepeat: true} // Create a LogData instance with the message and allow repeat
	LogTask <- data                                       // Send the LogData instance to the LogTask channel
}

// ErrorLogWorker is a background worker function that processes log messages from the LogTask channel.
func ErrorLogWorker() {
	var previousLogMessage string // Stores the last logged message to prevent repeats

	log.Println("Started netchan logging worker in background.") // Initial log indicating the worker has started

	for {
		select {
		case newLogTask := <-LogTask: // Receive a new log task from the channel
			if newLogTask.canRepeat == false {
				// If the log message should not repeat and it's different from the previous one
				if previousLogMessage != newLogTask.logMessage {
					log.Println(newLogTask.logMessage)         // Log the message
					previousLogMessage = newLogTask.logMessage // Update the last logged message
				}
			} else {
				log.Println(newLogTask.logMessage)         // Log the message
				previousLogMessage = newLogTask.logMessage // Update the last logged message
			}
		}
	}
}

// init starts the ErrorLogWorker function as a goroutine.
func init() {
	go ErrorLogWorker() // Start the ErrorLogWorker as a goroutine
}
