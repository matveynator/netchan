package transport

import (
	"fmt"
	"net"
	"time"
)

// Optional server authentication belongs to the node's bounded handshake
// workers. In particular, an eager SDK TLS engine must not serialize Accept.
type pendingServer struct {
	net.Conn
	options Options
}

func (*pendingServer) Read([]byte) (int, error) {
	return 0, fmt.Errorf("%w: server authentication must run in a handshake worker", ErrUnavailable)
}
func (*pendingServer) Write([]byte) (int, error) {
	return 0, fmt.Errorf("%w: server authentication must run in a handshake worker", ErrUnavailable)
}

// AuthenticateServer completes a deferred driver boundary after the caller has
// acquired a handshake slot and assigned the raw connection its deadline.
func AuthenticateServer(connection net.Conn, deadline time.Time) (net.Conn, error) {
	pending, ok := connection.(*pendingServer)
	if !ok {
		return connection, nil
	}
	canceled, finished, watcherDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	timer := time.NewTimer(time.Until(deadline))
	go func() {
		defer close(watcherDone)
		select {
		case <-timer.C:
			close(canceled)
		case <-pending.options.Done:
			close(canceled)
		case <-finished:
		}
	}()
	defer func() { close(finished); timer.Stop(); <-watcherDone }()
	options := pending.options
	ownerDone := options.OwnerDone
	if ownerDone == nil {
		ownerDone = options.Done
	}
	lifetime := NewLifetime(Options{Done: canceled, OwnerDone: ownerDone})
	secured, err := options.Security.Server(pending.Conn, options.TLS, lifetime.Done)
	if err != nil || secured == nil {
		lifetime.Close()
		_ = pending.Conn.Close()
		if err == nil {
			err = fmt.Errorf("%w: security backend returned no connection", ErrUnavailable)
		}
		return nil, err
	}
	if err = lifetime.Promote(); err != nil {
		lifetime.Close()
		_ = secured.Close()
		return nil, err
	}
	return OwnConnection(secured, lifetime), nil
}

// OwnConnection gives a successful transport to its long-lived owner and
// releases the native stream when that ownership ends, including before attach.
func OwnConnection(connection net.Conn, lifetime *Lifetime) net.Conn {
	go func() { <-lifetime.Done; _ = connection.Close() }()
	return &serverConnection{Conn: connection, lifetime: lifetime}
}

type serverConnection struct {
	net.Conn
	lifetime *Lifetime
}

func (connection *serverConnection) Underlying() net.Conn { return connection.Conn }
func (connection *serverConnection) Close() error {
	connection.lifetime.Close()
	return connection.Conn.Close()
}
