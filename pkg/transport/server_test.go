package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

type waitingSecurity struct{ started chan struct{} }

func (waitingSecurity) Client(net.Conn, *tls.Config, <-chan struct{}) (net.Conn, error) {
	return nil, ErrUnsupported
}
func (security waitingSecurity) Server(connection net.Conn, _ *tls.Config, done <-chan struct{}) (net.Conn, error) {
	security.started <- struct{}{}
	<-done
	return nil, net.ErrClosed
}

func TestServerAuthenticationIsDeferredAndBounded(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	ownerDone := make(chan struct{})
	defer close(ownerDone)
	security := waitingSecurity{started: make(chan struct{}, 1)}
	prepared := make(chan net.Conn, 1)
	go func() {
		connection, _ := SecureServer(local, Options{TLS: &tls.Config{MinVersion: tls.VersionTLS13}, Security: security, Done: ownerDone})
		prepared <- connection
	}()
	var pending net.Conn
	select {
	case pending = <-prepared:
	case <-time.After(time.Second):
		t.Fatal("SecureServer blocked Accept")
	}
	select {
	case <-security.started:
		t.Fatal("authentication started before admission")
	default:
	}
	finished := make(chan error, 1)
	go func() { _, err := AuthenticateServer(pending, time.Now().Add(30*time.Millisecond)); finished <- err }()
	select {
	case err := <-finished:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("authentication = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("eager authentication ignored handshake budget")
	}
}
