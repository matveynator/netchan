// Package tcp adapts authenticated TCP streams to the transport boundary.
package tcp

import (
	"context"
	"net"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

type Driver struct{}

func (Driver) Scheme() string { return "tcp" }

func (Driver) Listen(endpoint transport.Endpoint, options transport.Options) (net.Listener, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", endpoint.Host)
	if err != nil {
		return nil, err
	}
	endpoint.Host = listener.Addr().String()
	return &listenerAdapter{Listener: listener, endpoint: endpoint, options: options}, nil
}

type listenerAdapter struct {
	net.Listener
	endpoint transport.Endpoint
	options  transport.Options
}

func (listener *listenerAdapter) Addr() net.Addr { return listener.endpoint }

// The node bounds and cancels TLS handshakes; Accept must not serialize them.
func (listener *listenerAdapter) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return transport.SecureServer(connection, listener.options)
}

func (Driver) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	lifetime := transport.NewLifetime(options)
	failed := true
	defer func() {
		if failed {
			lifetime.Close()
		}
	}()
	options.Done = lifetime.Done
	// Context is confined to the standard-library dial and TLS boundaries.
	dialContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-options.Done:
			cancel()
		case <-finished:
		}
	}()
	defer func() { close(finished); cancel(); <-watcherDone }()
	dialer := &net.Dialer{}
	connection, err := dialer.DialContext(dialContext, "tcp", endpoint.Host)
	if err != nil {
		return nil, err
	}
	options.TLS = transport.ClientTLS(options.TLS, endpoint, options.PeerID)
	secured, err := transport.SecureClient(connection, options)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err = lifetime.Promote(); err != nil {
		_ = secured.Close()
		return nil, err
	}
	failed = false
	return transport.OwnConnection(secured, lifetime), nil
}
