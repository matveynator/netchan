// Package rfcomm authenticates native Bluetooth Classic streams with TLS 1.3.
package rfcomm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

// Radio is the native stream and SDP boundary. Implementations must release
// blocked native operations when done closes and transfer accepted streams.
type Radio interface {
	Listen(transport.Endpoint, string, string, <-chan struct{}) (net.Listener, error)
	Dial(transport.Endpoint, string, <-chan struct{}) (net.Conn, error)
	Discover(string, chan<- Record, <-chan struct{}) error
}

type Record struct{ Device, Metadata string }
type Driver struct{ Radio Radio }

func (Driver) Scheme() string { return "rfcomm" }
func (driver Driver) radio() (Radio, error) {
	if driver.Radio != nil {
		return driver.Radio, nil
	}
	return systemRadio()
}

func UUID(service string) string {
	digest := sha256.Sum256([]byte("netchan/2/rfcomm/" + service))
	digest[6] = digest[6]&15 | 0x50
	digest[8] = digest[8]&63 | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func metadata(service, peer, instance string) string {
	return "netchan/2/" + url.PathEscape(service) + "/" + peer + "/" + instance
}

func parseMetadata(encoded string) (service, peer, instance string, err error) {
	if len(encoded) > 320 {
		return "", "", "", errors.New("netchan: oversized SDP metadata")
	}
	parts := strings.Split(encoded, "/")
	if len(parts) != 5 || parts[0] != "netchan" || parts[1] != "2" {
		return "", "", "", errors.New("netchan: invalid SDP metadata")
	}
	service, err = url.PathUnescape(parts[2])
	if err != nil || len(service) == 0 || len(service) > 63 || strings.ContainsAny(service, "/\x00") {
		return "", "", "", errors.New("netchan: invalid SDP service")
	}
	key, err := hex.DecodeString(parts[3])
	if err != nil || len(key) != 32 {
		return "", "", "", errors.New("netchan: invalid SDP peer")
	}
	nonce, err := hex.DecodeString(parts[4])
	if err != nil || len(nonce) != 16 {
		return "", "", "", errors.New("netchan: invalid SDP instance")
	}
	return service, parts[3], parts[4], nil
}

func (driver Driver) Listen(endpoint transport.Endpoint, options transport.Options) (net.Listener, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	radio, err := driver.radio()
	if err != nil {
		return nil, err
	}
	if options.PeerID == "" {
		options.PeerID, err = transport.PeerID(options.TLS)
		if err != nil {
			return nil, err
		}
	}
	if options.InstanceID == "" {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		options.InstanceID = hex.EncodeToString(nonce[:])
	}
	socket, err := radio.Listen(endpoint, UUID(endpoint.Service), metadata(endpoint.Service, options.PeerID, options.InstanceID), options.Done)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", transport.ErrUnavailable, err)
	}
	return &listenerAdapter{Listener: socket, options: options}, nil
}

type listenerAdapter struct {
	net.Listener
	options transport.Options
}

func (listener *listenerAdapter) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	secured, err := transport.SecureServer(connection, listener.options)
	if err != nil {
		_ = connection.Close()
	}
	return secured, err
}

type ownedConnection struct {
	net.Conn
	lifetime *transport.Lifetime
}

func (connection *ownedConnection) Close() error {
	err := connection.Conn.Close()
	connection.lifetime.Close()
	return err
}
func (connection *ownedConnection) Underlying() net.Conn { return connection.Conn }

func (driver Driver) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	if endpoint.Host == "" {
		return nil, errors.New("netchan: RFCOMM requires a device identifier")
	}
	radio, err := driver.radio()
	if err != nil {
		return nil, err
	}
	lifetime := transport.NewLifetime(options)
	failed := true
	defer func() {
		if failed {
			lifetime.Close()
		}
	}()
	connection, err := radio.Dial(endpoint, UUID(endpoint.Service), lifetime.Done)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", transport.ErrUnavailable, err)
	}
	options.TLS = transport.ClientTLS(options.TLS, endpoint, options.PeerID)
	options.Done = lifetime.Done
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
	return &ownedConnection{Conn: secured, lifetime: lifetime}, nil
}

func (driver Driver) Discover(service string, results chan<- transport.Candidate, done <-chan struct{}) error {
	radio, err := driver.radio()
	if err != nil {
		return err
	}
	records := make(chan Record, 16)
	finished := make(chan error, 1)
	go func() { finished <- radio.Discover(UUID(service), records, done) }()
	for {
		select {
		case record := <-records:
			name, peer, instance, decodeErr := parseMetadata(record.Metadata)
			if decodeErr != nil || name != service {
				continue
			}
			select {
			case results <- transport.Candidate{Endpoint: transport.Endpoint{Scheme: "rfcomm", Host: record.Device, Service: service}, PeerID: peer, InstanceID: instance, Expires: time.Now().Add(30 * time.Second)}:
			case <-done:
			}
		case err := <-finished:
			return err
		case <-done:
			for {
				select {
				case <-records:
				case err := <-finished:
					return err
				}
			}
		}
	}
}
