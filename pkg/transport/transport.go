// Package transport defines the physical boundary beneath protocol v2.
// Drivers belong to a caller, rather than a process-wide mutable registry.
package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

var (
	ErrUnsupported   = errors.New("netchan: unsupported transport")
	ErrUnavailable   = errors.New("netchan: transport unavailable")
	ErrPeerNotFound  = errors.New("netchan: peer not found")
	ErrAmbiguousPeer = errors.New("netchan: ambiguous peer")
)

// Endpoint separates physical addressing from logical service discovery.
type Endpoint struct {
	Scheme  string
	Host    string
	Service string
	Legacy  bool
}

func Parse(address string) (Endpoint, error) {
	if !strings.Contains(address, "://") {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return Endpoint{}, fmt.Errorf("netchan: invalid TCP address: %w", err)
		}
		return Endpoint{Scheme: "tcp", Host: address, Legacy: true}, nil
	}
	scheme, remainder, _ := strings.Cut(address, "://")
	if scheme == "udp" {
		scheme = "quic"
	}
	endpoint := Endpoint{Scheme: scheme}
	if strings.ContainsAny(remainder, "?#") {
		return Endpoint{}, errors.New("netchan: transport addresses cannot contain credentials, queries, or fragments")
	}
	switch scheme {
	case "tcp", "quic":
		if strings.Contains(remainder, "@") {
			return Endpoint{}, errors.New("netchan: transport addresses cannot contain credentials")
		}
		if strings.Contains(remainder, "/") {
			return Endpoint{}, errors.New("netchan: IP transport address cannot contain a path")
		}
		if _, _, err := net.SplitHostPort(remainder); err != nil {
			return Endpoint{}, fmt.Errorf("netchan: invalid %s address: %w", scheme, err)
		}
		endpoint.Host = remainder
	case "rfcomm", "ble", "nearby":
		host, escapedService, found := strings.Cut(remainder, "/")
		service, err := url.PathUnescape(escapedService)
		if !found || err != nil || service == "" || len(service) > 63 || strings.ContainsAny(service, "/\x00") {
			return Endpoint{}, errors.New("netchan: transport address requires one service name of at most 63 bytes")
		}
		if strings.ContainsAny(host, "\x00%[]\\@") {
			return Endpoint{}, errors.New("netchan: invalid device or peer identifier")
		}
		endpoint.Host, endpoint.Service = host, service
	default:
		return Endpoint{}, fmt.Errorf("%w: %s", ErrUnsupported, scheme)
	}
	return endpoint, nil
}

func (endpoint Endpoint) String() string {
	if endpoint.Legacy {
		return endpoint.Host
	}
	if endpoint.Service != "" {
		return endpoint.Scheme + "://" + endpoint.Host + "/" + url.PathEscape(endpoint.Service)
	}
	return endpoint.Scheme + "://" + endpoint.Host
}

func (endpoint Endpoint) Network() string { return endpoint.Scheme }

// Options are immutable after transfer to a driver. Done owns the whole attempt.
type Options struct {
	TLS        *tls.Config
	Done       <-chan struct{}
	OwnerDone  <-chan struct{}
	PeerID     string
	InstanceID string
	Security   Security
}

// Security adapts a target's verified TLS engine to an arbitrary stream.
// Client must complete authentication; Server may authenticate lazily on I/O.
type Security interface {
	Client(net.Conn, *tls.Config, <-chan struct{}) (net.Conn, error)
	Server(net.Conn, *tls.Config, <-chan struct{}) (net.Conn, error)
}

func SecureClient(connection net.Conn, options Options) (net.Conn, error) {
	if options.Security != nil {
		return options.Security.Client(connection, options.TLS, options.Done)
	}
	return defaultSecureClient(connection, options)
}

func SecureServer(connection net.Conn, options Options) (net.Conn, error) {
	if options.Security != nil {
		return &pendingServer{Conn: connection, options: options}, nil
	}
	return defaultSecureServer(connection, options)
}

// Underlying preserves optional stream capabilities through TLS and discovery
// wrappers without requiring those wrappers to share mutable protocol state.
func Underlying(connection net.Conn) net.Conn {
	if wrapped, ok := connection.(interface{ Underlying() net.Conn }); ok {
		return wrapped.Underlying()
	}
	if wrapped, ok := connection.(interface{ NetConn() net.Conn }); ok {
		return wrapped.NetConn()
	}
	return nil
}

func Drain(connection net.Conn, deadline time.Time) error {
	for depth := 0; connection != nil && depth < 8; depth++ {
		if graceful, ok := connection.(Graceful); ok {
			return graceful.Drain(deadline)
		}
		connection = Underlying(connection)
	}
	return nil
}

func PeerFinished(connection net.Conn) {
	for depth := 0; connection != nil && depth < 8; depth++ {
		if terminal, ok := connection.(Terminal); ok {
			terminal.PeerFinished()
			return
		}
		connection = Underlying(connection)
	}
}

func ActivityChannel(connection net.Conn) <-chan struct{} {
	for depth := 0; connection != nil && depth < 8; depth++ {
		if active, ok := connection.(Activity); ok {
			return active.Activity()
		}
		connection = Underlying(connection)
	}
	return nil
}

// Driver returns authenticated streams. TLS is performed here, before a stream
// can send HELLO, so competing physical candidates cannot replace a session.
type Driver interface {
	Scheme() string
	Listen(Endpoint, Options) (net.Listener, error)
	Dial(Endpoint, Options) (net.Conn, error)
}

// Candidate is an untrusted discovery hint until TLS verifies its identity.
type Candidate struct {
	Endpoint   Endpoint
	PeerID     string
	InstanceID string
	Expires    time.Time
}

type Discoverer interface {
	Discover(service string, results chan<- Candidate, done <-chan struct{}) error
}

// Graceful allows packet streams to finish their send side before destruction.
type Graceful interface {
	Drain(deadline time.Time) error
}

// Terminal lets the frame reader acknowledge peer shutdown independently of
// the session actor, which may itself be draining its final control frames.
type Terminal interface{ PeerFinished() }

// Activity reports authenticated link progress during a partially received frame.
type Activity interface{ Activity() <-chan struct{} }

func PeerID(configuration *tls.Config) (string, error) {
	if configuration == nil || len(configuration.Certificates) == 0 || configuration.Certificates[0].Leaf == nil && len(configuration.Certificates[0].Certificate) == 0 {
		return "", errors.New("netchan: nearby listener requires a static TLS certificate")
	}
	certificate := configuration.Certificates[0].Leaf
	if certificate == nil {
		var err error
		certificate, err = parseCertificate(configuration.Certificates[0].Certificate[0])
		if err != nil {
			return "", err
		}
	}
	return PublicKeyID(certificate.RawSubjectPublicKeyInfo), nil
}

func PublicKeyID(publicKey []byte) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}

// ClientTLS retains the caller's trust policy and adds a pin when discovery
// selects a peer. An advertised fingerprint never enables InsecureSkipVerify.
func ClientTLS(configuration *tls.Config, endpoint Endpoint, peerID string) *tls.Config {
	cloned := configuration.Clone()
	if cloned.MinVersion < tls.VersionTLS13 {
		cloned.MinVersion = tls.VersionTLS13
	}
	if cloned.ServerName == "" && (endpoint.Scheme == "tcp" || endpoint.Scheme == "quic") {
		cloned.ServerName, _, _ = net.SplitHostPort(endpoint.Host)
	}
	if peerID != "" {
		previous := cloned.VerifyConnection
		cloned.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || PublicKeyID(state.PeerCertificates[0].RawSubjectPublicKeyInfo) != peerID {
				return errors.New("netchan: TLS peer identity mismatch")
			}
			if previous != nil {
				return previous(state)
			}
			return nil
		}
	}
	return cloned
}
