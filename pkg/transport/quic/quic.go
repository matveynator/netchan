//go:build !tinygo

// Package quic provides one ordered bidirectional stream over encrypted UDP.
package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
	quicgo "github.com/quic-go/quic-go"
)

type Driver struct{}

func (Driver) Scheme() string { return "quic" }

func configuration(configuration *tls.Config) *tls.Config {
	cloned := configuration.Clone()
	cloned.MinVersion = tls.VersionTLS13
	cloned.NextProtos = []string{"netchan/2"}
	return cloned
}

func (Driver) Listen(endpoint transport.Endpoint, options transport.Options) (net.Listener, error) {
	listener, err := quicgo.ListenAddr(endpoint.Host, configuration(options.TLS), &quicgo.Config{MaxIncomingStreams: 1, MaxIncomingUniStreams: -1, HandshakeIdleTimeout: 15 * time.Second})
	if err != nil {
		return nil, err
	}
	endpoint.Host = listener.Addr().String()
	acceptContext, cancel := context.WithCancel(context.Background())
	return &listenerAdapter{listener: listener, endpoint: endpoint, acceptContext: acceptContext, cancel: cancel}, nil
}

type listenerAdapter struct {
	listener      *quicgo.Listener
	endpoint      transport.Endpoint
	acceptContext context.Context
	cancel        context.CancelFunc
}

func (listener *listenerAdapter) Addr() net.Addr { return listener.endpoint }
func (listener *listenerAdapter) Close() error   { listener.cancel(); return listener.listener.Close() }

func (listener *listenerAdapter) Accept() (net.Conn, error) {
	connection, err := listener.listener.Accept(listener.acceptContext)
	if err != nil {
		return nil, net.ErrClosed
	}
	// Accepting the stream is deferred to Read so an authenticated peer that
	// never opens a stream cannot occupy the listener's single accept loop.
	return newConnection(connection, nil), nil
}

func (Driver) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
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
	connection, err := quicgo.DialAddr(dialContext, endpoint.Host, configuration(transport.ClientTLS(options.TLS, endpoint, options.PeerID)), &quicgo.Config{MaxIncomingStreams: -1, MaxIncomingUniStreams: -1, HandshakeIdleTimeout: 15 * time.Second})
	if err != nil {
		return nil, err
	}
	stream, err := connection.OpenStreamSync(dialContext)
	if err != nil {
		_ = connection.CloseWithError(0, "stream open failed")
		return nil, err
	}
	return newConnection(connection, stream), nil
}

type streamResult struct {
	stream *quicgo.Stream
	err    error
}

type connectionAdapter struct {
	connection   *quicgo.Conn
	stream       <-chan streamResult
	peerFinished chan struct{}
}

func newConnection(connection *quicgo.Conn, stream *quicgo.Stream) *connectionAdapter {
	ready := make(chan streamResult, 1)
	if stream != nil {
		ready <- streamResult{stream: stream}
	} else {
		go func() {
			acceptContext, cancel := context.WithTimeout(connection.Context(), 15*time.Second)
			defer cancel()
			accepted, err := connection.AcceptStream(acceptContext)
			ready <- streamResult{stream: accepted, err: err}
		}()
	}
	// Closing a one-shot broadcast channel publishes the immutable result to
	// concurrent readers and writers without a shared mutable stream pointer.
	published := make(chan streamResult)
	go func() {
		result := <-ready
		for {
			select {
			case published <- result:
			case <-connection.Context().Done():
				close(published)
				return
			}
		}
	}()
	return &connectionAdapter{connection: connection, stream: published, peerFinished: make(chan struct{}, 1)}
}

func (connection *connectionAdapter) PeerFinished() {
	select {
	case connection.peerFinished <- struct{}{}:
	default:
	}
}

func (connection *connectionAdapter) getStream() (*quicgo.Stream, error) {
	result, open := <-connection.stream
	if !open {
		return nil, net.ErrClosed
	}
	return result.stream, result.err
}

func (connection *connectionAdapter) Read(destination []byte) (int, error) {
	stream, err := connection.getStream()
	if err != nil {
		return 0, err
	}
	return stream.Read(destination)
}
func (connection *connectionAdapter) Write(payload []byte) (int, error) {
	stream, err := connection.getStream()
	if err != nil {
		return 0, err
	}
	return stream.Write(payload)
}
func (connection *connectionAdapter) Close() error {
	return connection.connection.CloseWithError(0, "closed")
}
func (connection *connectionAdapter) LocalAddr() net.Addr  { return connection.connection.LocalAddr() }
func (connection *connectionAdapter) RemoteAddr() net.Addr { return connection.connection.RemoteAddr() }
func (connection *connectionAdapter) SetDeadline(deadline time.Time) error {
	stream, err := connection.getStream()
	if err != nil {
		return err
	}
	return stream.SetDeadline(deadline)
}
func (connection *connectionAdapter) SetReadDeadline(deadline time.Time) error {
	stream, err := connection.getStream()
	if err != nil {
		return err
	}
	return stream.SetReadDeadline(deadline)
}
func (connection *connectionAdapter) SetWriteDeadline(deadline time.Time) error {
	stream, err := connection.getStream()
	if err != nil {
		return err
	}
	return stream.SetWriteDeadline(deadline)
}

func (connection *connectionAdapter) Drain(deadline time.Time) error {
	stream, err := connection.getStream()
	if err != nil {
		return err
	}
	if err = stream.Close(); err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-connection.peerFinished:
		return nil
	case <-connection.connection.Context().Done():
		var closed *quicgo.ApplicationError
		if errors.As(context.Cause(connection.connection.Context()), &closed) && closed.ErrorCode == 0 {
			return nil
		}
		return context.Cause(connection.connection.Context())
	case <-timer.C:
		return nil
	}
}
