package packet

import (
	"bytes"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

type testAddress string

func (address testAddress) Network() string { return "packet" }
func (address testAddress) String() string  { return string(address) }

type testLink struct {
	incoming chan []byte
	outgoing chan []byte
	done     chan struct{}
	stop     chan struct{}
	mtu      int
	faults   bool
}

func (link *testLink) Send(payload []byte, done <-chan struct{}) error {
	select {
	case link.outgoing <- append([]byte(nil), payload...):
		return nil
	case <-done:
		return net.ErrClosed
	case <-link.done:
		return net.ErrClosed
	}
}
func (link *testLink) Packets() <-chan []byte { return link.incoming }
func (link *testLink) Done() <-chan struct{}  { return link.done }
func (link *testLink) MTU() int               { return link.mtu }
func (link *testLink) LocalAddr() net.Addr    { return testAddress("local") }
func (link *testLink) RemoteAddr() net.Addr   { return testAddress("remote") }
func (link *testLink) Close() error {
	select {
	case link.stop <- struct{}{}:
	case <-link.done:
	}
	<-link.done
	return nil
}

func linkPair(t *testing.T, mtu int, faults bool) (Link, Link) {
	t.Helper()
	first := &testLink{incoming: make(chan []byte, 16), outgoing: make(chan []byte), done: make(chan struct{}), stop: make(chan struct{}), mtu: mtu}
	second := &testLink{incoming: make(chan []byte, 16), outgoing: make(chan []byte), done: make(chan struct{}), stop: make(chan struct{}), mtu: mtu}
	for _, direction := range []struct{ source, destination *testLink }{{first, second}, {second, first}} {
		go func(source, destination *testLink) {
			defer close(source.done)
			count := 0
			var delayed []byte
			forward := func(payload []byte) bool {
				select {
				case destination.incoming <- payload:
					return true
				case <-source.stop:
					return false
				case <-destination.done:
					return false
				}
			}
			for {
				select {
				case payload := <-source.outgoing:
					count++
					if faults && count%13 == 0 {
						continue
					}
					if faults && count%17 == 0 && delayed == nil {
						delayed = payload
						continue
					}
					if !forward(payload) {
						return
					}
					if faults && count%19 == 0 && !forward(payload) {
						return
					}
					if delayed != nil {
						if !forward(delayed) {
							return
						}
						delayed = nil
					}
				case <-source.stop:
					return
				case <-destination.done:
					return
				}
			}
		}(direction.source, direction.destination)
	}
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	return first, second
}

func TestStreamRecoversLostDuplicatedAndReorderedPackets(t *testing.T) {
	first, second := linkPair(t, 20, true)
	client, err := Open(first)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := Open(second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))
	_ = server.SetDeadline(time.Now().Add(15 * time.Second))
	payload := bytes.Repeat([]byte("ordered packet stream"), 128)
	written := make(chan error, 1)
	go func() { _, err := client.Write(payload); written <- err }()
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(server, received); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, received) {
		t.Fatal("stream changed bytes or delivery order")
	}
	drained := make(chan error, 1)
	go func() { drained <- client.Drain(time.Now().Add(time.Second)) }()
	if _, err := server.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after FIN = %v", err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestReadDeadlineCanBeResetAndCloseInterruptsRead(t *testing.T) {
	first, second := linkPair(t, 64, false)
	client, _ := Open(first)
	defer client.Close()
	server, _ := Open(second)
	defer server.Close()
	_ = server.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := server.Read(make([]byte, 1)); !os.IsTimeout(err) {
		t.Fatalf("read deadline: %v", err)
	}
	_ = server.SetReadDeadline(time.Time{})
	read := make(chan error, 1)
	go func() { _, err := server.Read(make([]byte, 1)); read <- err }()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel read")
	}
}
