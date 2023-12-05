// Package netchan provides functionalities for network channel operations,
// including a specialized logging system. This package defines structures and
// functions to manage and format log messages efficiently.
package netchan

import (
	"log" // Package log provides a simple logging interface.
)

// LogData represents a single log entry with a message and a repeat flag.
// It encapsulates the data necessary for processing log messages.
type LogData struct {
	logMessage string // The log message to be recorded.
	canRepeat  bool   // Flag indicating if the message can be logged repeatedly.
	// processId string // Uncomment if process ID is needed in future implementations.
}

// LogTask is a channel for sending log tasks to be processed by the logging system.
// This channel is used to asynchronously handle log messages.
var LogTask chan LogData

// init function sets up the logging format and initializes the LogTask channel.
// This function runs automatically on package initialization.
func init() {
	// Configure the log package to output the date, time, and microseconds.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Initialize LogTask as an unbuffered channel for handling log data.
	LogTask = make(chan LogData)
}

// Printonce sends a log message to the LogTask channel with a repeat flag set to false.
// This function ensures that the message is logged only once.
func Printonce(message string) {
	data := LogData{logMessage: message, canRepeat: false}
	LogTask <- data
}

// Println sends a log message to the LogTask channel with a repeat flag set to true.
// This function allows the message to be logged repeatedly.
func Println(message string) {
	data := LogData{logMessage: message, canRepeat: true}
	LogTask <- data
}

// ErrorLogWorker is a function meant to run as a goroutine for processing log tasks.
// It reads from the LogTask channel and logs messages, avoiding repeat logs if indicated.
func ErrorLogWorker() {
	var previousLogMessage string // Stores the last logged message to prevent repeats.

	// Log a startup message to indicate the logging worker is running.
	log.Println("Started netchan logging worker in background.")

	// Infinite loop to continuously process incoming log tasks.
	for {
		select {
		case newLogTask := <-LogTask: // Receive a new log task.
			// Check if the new log task is set to not repeat.
			if newLogTask.canRepeat == false {
				// Log the message only if it's different from the previous message.
				if previousLogMessage != newLogTask.logMessage {
					log.Println(newLogTask.logMessage)
					previousLogMessage = newLogTask.logMessage
				}
			} else {
				// If the task can repeat, log it and update the previous message.
				log.Println(newLogTask.logMessage)
				previousLogMessage = newLogTask.logMessage
			}
		}
	}
}

// Second init function starts the ErrorLogWorker as a goroutine.
// This setup ensures that logging is active throughout the package's lifecycle.
func init() {
	go ErrorLogWorker()
}
