//go:build !tinygo

package netchan

import (
	"crypto/tls"
	"net"
	"os"
	"testing"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

type eagerTestSecurity struct{ started chan struct{} }

func (eagerTestSecurity) Client(connection net.Conn, configuration *tls.Config, done <-chan struct{}) (net.Conn, error) {
	secured, err := transport.SecureClient(connection, transport.Options{TLS: configuration, Done: done})
	if err != nil {
		return nil, err
	}
	// Keep the backend tied to cancellation, like a device TLS session actor.
	go func() { <-done; _ = secured.Close() }()
	return secured, nil
}
func (security eagerTestSecurity) Server(connection net.Conn, configuration *tls.Config, done <-chan struct{}) (net.Conn, error) {
	select {
	case security.started <- struct{}{}:
	case <-done:
		return nil, net.ErrClosed
	}
	secured := tls.Server(connection, configuration)
	if err := secured.Handshake(); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return secured, nil
}
func TestPublicEagerSecurityDoesNotBlockAccept(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow local listeners")
	}
	serverTLS, clientTLS := testTLSConfigurations(t)
	security := eagerTestSecurity{started: make(chan struct{}, 2)}
	listener, err := Listen[int]("127.0.0.1:0", Config{TLS: serverTLS, Security: security})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	idle, err := net.Dial("tcp", listener.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	receiveWithTimeout(t, security.started)
	type result struct {
		channel *Channel[int]
		err     error
	}
	connected := make(chan result, 1)
	go func() {
		channel, err := Dial[int](listener.Address, Config{TLS: clientTLS, Security: security})
		connected <- result{channel: channel, err: err}
	}()
	select {
	case result := <-connected:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.channel.Abort()
	case <-time.After(3 * time.Second):
		t.Fatal("eager server blocked the next client")
	}
	server := receiveWithTimeout(t, listener.Channels)
	if server == nil {
		t.Fatal("authenticated client was not accepted")
	}
}

func TestPublicNearbySecurityLifetime(t *testing.T) {
	testPublicNearbyDiscovery(t, eagerTestSecurity{started: make(chan struct{}, 2)})
}
