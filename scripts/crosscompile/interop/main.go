//go:build netchan_interop

package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/matveynator/netchan/v2"
	"github.com/matveynator/netchan/v2/internal/interop"
	"github.com/matveynator/netchan/v2/pkg/transport"
)

type task = interop.Task

// The Go test controller owns verified TLS and transfers its application stream
// through private anonymous pipes, like an external device TLS offload engine.
// This adapter belongs exclusively to the compiler interoperability fixture.
type broker struct{}

func (broker) Scheme() string { return "tcp" }
func (broker) Listen(transport.Endpoint, transport.Options) (net.Listener, error) {
	return nil, transport.ErrUnsupported
}
func (broker) Dial(transport.Endpoint, transport.Options) (net.Conn, error) {
	return brokerStream{}, nil
}

type brokerStream struct{}

func (brokerStream) Read(destination []byte) (int, error) { return os.Stdin.Read(destination) }
func (brokerStream) Write(payload []byte) (int, error)    { return os.Stdout.Write(payload) }
func (brokerStream) Close() error                         { return nil }
func (brokerStream) LocalAddr() net.Addr                  { return transport.Endpoint{Scheme: "pipe", Host: "tinygo"} }
func (brokerStream) RemoteAddr() net.Addr {
	return transport.Endpoint{Scheme: "pipe", Host: "tls-broker"}
}
func (brokerStream) SetDeadline(time.Time) error      { return nil }
func (brokerStream) SetReadDeadline(time.Time) error  { return nil }
func (brokerStream) SetWriteDeadline(time.Time) error { return nil }

func exchange() error {
	channel, err := netchan.Dial[task]("tcp://localhost:1", netchan.Config{TLS: &tls.Config{MinVersion: tls.VersionTLS13}, Transports: []transport.Driver{broker{}}, ChannelTypes: []netchan.ChannelType{netchan.ChannelElement[string](), netchan.ChannelElement[struct{}]()}})
	if err != nil {
		return err
	}
	reply, stop := make(chan string), make(chan struct{})
	delivered := make(chan error, 1)
	go func() { delivered <- channel.Deliver(task{Name: "tinygo", Reply: reply, Stop: stop}) }()
	answer := <-reply
	if answer != "Go TLS -> TinyGo" {
		return fmt.Errorf("unexpected response %q", answer)
	}
	if err = <-delivered; err != nil {
		return err
	}
	close(stop)
	return channel.Close()
}
func main() {
	if err := exchange(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
