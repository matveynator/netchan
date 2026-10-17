package netchan

import (
	"net"
	"testing"
	"time"
)

type progressConnection struct {
	net.Conn
	progress  chan struct{}
	deadlines chan time.Time
}

func (connection progressConnection) Activity() <-chan struct{} { return connection.progress }
func (connection progressConnection) SetWriteDeadline(deadline time.Time) error {
	connection.deadlines <- deadline
	return connection.Conn.SetWriteDeadline(deadline)
}

func TestPhysicalWriteProgressPreservesAbsoluteDeadlines(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "stall budget"
		if absolute {
			name = "absolute deadline"
		}
		t.Run(name, func(t *testing.T) {
			local, remote := net.Pipe()
			defer remote.Close()
			connection := progressConnection{Conn: local, progress: make(chan struct{}, 1), deadlines: make(chan time.Time, 8)}
			sessionDone := make(chan struct{})
			defer close(sessionDone)
			physical := runPhysicalConnection(connection, make(chan physicalConnectionEvent), sessionDone, sessionDone)
			deadline := time.Time{}
			if absolute {
				deadline = time.Now().Add(3 * time.Second)
			}
			written := make(chan error, 1)
			go func() {
				written <- physical.sendAndWaitUntil(networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "test"}, deadline)
			}()
			initial := receiveWithTimeout(t, connection.deadlines)
			connection.progress <- struct{}{}
			if absolute {
				select {
				case changed := <-connection.deadlines:
					t.Fatalf("progress replaced absolute deadline %v with %v", initial, changed)
				case <-time.After(30 * time.Millisecond):
				}
			} else {
				updated := receiveWithTimeout(t, connection.deadlines)
				if !updated.After(initial) {
					t.Fatalf("stall deadline did not advance: %v -> %v", initial, updated)
				}
			}
			_ = remote.Close()
			if err := receiveWithTimeout(t, written); err == nil {
				t.Fatal("blocked write succeeded without a reader")
			}
		})
	}
}
