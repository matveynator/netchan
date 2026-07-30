package netchan

import (
	"log"
	"strings"
	"testing"
	"time"
)

type logChannelWriter struct {
	writes chan string
}

func (writer logChannelWriter) Write(message []byte) (int, error) {
	writer.writes <- string(message)
	return len(message), nil
}

func TestLogWorkerStartsWithoutStartupMessageAndSuppressesDuplicate(t *testing.T) {
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	writes := make(chan string, 4)
	log.SetOutput(logChannelWriter{writes: writes})
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})

	const message = "netchan lazy logger test"
	Printonce(message)
	select {
	case writtenMessage := <-writes:
		if strings.TrimSpace(writtenMessage) != message {
			t.Fatalf("first log message = %q", writtenMessage)
		}
	case <-time.After(time.Second):
		t.Fatal("first log message was not written")
	}

	Printonce(message)
	select {
	case writtenMessage := <-writes:
		t.Fatalf("duplicate message was written: %q", writtenMessage)
	case <-time.After(50 * time.Millisecond):
	}
}
