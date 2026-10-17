// Package ble runs protocol v2 over a reliable GATT packet stream.
package ble

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/packet"
)

const envelopeSize = 5
const messageData = 0
const messageOpen = 1
const messageClose = 2
const messageReady = 3
const metadataMarker = 0x6d
const metadataPartSize = 13

// Radio isolates native GATT APIs and board initialization from the channel
// protocol. A supplied Radio is owned by this Driver, never a global registry.
type Radio interface {
	Listen(serviceUUID string, metadata []byte, done <-chan struct{}) (Peripheral, error)
	Dial(deviceID, serviceUUID string, done <-chan struct{}) (Central, error)
	Scan(serviceUUID string, devices chan<- string, done <-chan struct{}) error
}

type Received struct {
	Peer    string
	Payload []byte
}
type Peripheral interface {
	Packets() <-chan Received
	Send(peer string, payload []byte, done <-chan struct{}) error
	Address() string
	Close() error
}
type Central interface {
	Packets() <-chan []byte
	Send(payload []byte, done <-chan struct{}) error
	Metadata() []byte
	Address() string
	Close() error
}

type Driver struct{ Radio Radio }

func (Driver) Scheme() string { return "ble" }
func (driver Driver) radio(done <-chan struct{}) (Radio, error) {
	if driver.Radio != nil {
		return driver.Radio, nil
	}
	return systemRadio(done)
}

func NewDriver(done <-chan struct{}) (Driver, error) {
	radio, err := systemRadio(done)
	return Driver{Radio: radio}, err
}

// UUID is stable across compilers and architectures. Each characteristic uses
// a separate discriminator, keeping the service namespace independent of names.
func UUID(service string, discriminator byte) string {
	digest := sha256.Sum256(append([]byte("netchan/2/ble/"+service), discriminator))
	digest[6] = (digest[6] & 15) | 0x50
	digest[8] = (digest[8] & 63) | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// Metadata fits in a GATT characteristic; advertisements only need a UUID.
func EncodeMetadata(service, peerID, instanceID string) ([]byte, error) {
	peer, err := hex.DecodeString(peerID)
	if err != nil || len(peer) != 32 {
		return nil, errors.New("netchan: BLE requires a TLS public-key fingerprint")
	}
	instance, err := hex.DecodeString(instanceID)
	if err != nil || len(instance) != 16 {
		return nil, errors.New("netchan: invalid BLE instance identifier")
	}
	serviceLength := len(service)
	if serviceLength > 63 {
		return nil, errors.New("netchan: invalid BLE service name")
	}
	if serviceLength == 0 || strings.ContainsAny(service, "/\x00") {
		return nil, errors.New("netchan: invalid BLE service name")
	}
	return append(append(append([]byte{2, byte(serviceLength)}, peer...), instance...), []byte(service)...), nil
}

func DecodeMetadata(encoded []byte) (service, peerID, instanceID string, err error) {
	if len(encoded) < 50 || encoded[0] != 2 || int(encoded[1]) != len(encoded)-50 || encoded[1] == 0 || encoded[1] > 63 {
		return "", "", "", errors.New("netchan: invalid BLE metadata")
	}
	return string(encoded[50:]), hex.EncodeToString(encoded[2:34]), hex.EncodeToString(encoded[34:50]), nil
}

func (driver Driver) Listen(endpoint transport.Endpoint, options transport.Options) (net.Listener, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	radio, err := driver.radio(options.Done)
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
		identifier := make([]byte, 16)
		if _, err = rand.Read(identifier); err != nil {
			return nil, err
		}
		options.InstanceID = hex.EncodeToString(identifier)
	}
	metadata, err := EncodeMetadata(endpoint.Service, options.PeerID, options.InstanceID)
	if err != nil {
		return nil, err
	}
	peripheral, err := radio.Listen(UUID(endpoint.Service, 0), metadata, options.Done)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", transport.ErrUnavailable, err)
	}
	endpoint.Host = peripheral.Address()
	listener := &listenerAdapter{endpoint: endpoint, peripheral: peripheral, metadata: metadata, options: options, accepted: make(chan net.Conn), commands: make(chan any), done: make(chan struct{})}
	go listener.run()
	return listener, nil
}

type listenerAdapter struct {
	endpoint   transport.Endpoint
	peripheral Peripheral
	metadata   []byte
	options    transport.Options
	accepted   chan net.Conn
	commands   chan any
	done       chan struct{}
}

func (listener *listenerAdapter) Addr() net.Addr { return listener.endpoint }
func (listener *listenerAdapter) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.accepted:
		return transport.SecureServer(connection, listener.options)
	case <-listener.done:
		return nil, net.ErrClosed
	}
}
func (listener *listenerAdapter) Close() error {
	select {
	case listener.commands <- closeListener{}:
	case <-listener.done:
	}
	<-listener.done
	return nil
}

type closeListener struct{}
type sendPacket struct {
	link    *linkAdapter
	payload []byte
	reply   chan error
	done    <-chan struct{}
}
type closeLink struct {
	link  *linkAdapter
	reply chan struct{}
}

type linkAdapter struct {
	peer          string
	identifier    uint32
	incoming      chan []byte
	done          chan struct{}
	commands      chan any
	ownerDone     <-chan struct{}
	local, remote transport.Endpoint
}

func (link *linkAdapter) Packets() <-chan []byte { return link.incoming }
func (link *linkAdapter) Done() <-chan struct{}  { return link.done }
func (link *linkAdapter) MTU() int               { return 20 - envelopeSize }
func (link *linkAdapter) LocalAddr() net.Addr    { return link.local }
func (link *linkAdapter) RemoteAddr() net.Addr   { return link.remote }
func (link *linkAdapter) Send(payload []byte, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case link.commands <- sendPacket{link: link, payload: append([]byte(nil), payload...), reply: reply, done: done}:
	case <-done:
		return net.ErrClosed
	case <-link.done:
		return net.ErrClosed
	case <-link.ownerDone:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-done:
		return net.ErrClosed
	case <-link.done:
		return net.ErrClosed
	case <-link.ownerDone:
		return net.ErrClosed
	}
}
func (link *linkAdapter) Close() error {
	reply := make(chan struct{}, 1)
	select {
	case link.commands <- closeLink{link: link, reply: reply}:
	case <-link.done:
		return nil
	case <-link.ownerDone:
		return nil
	}
	select {
	case <-reply:
	case <-link.ownerDone:
	}
	return nil
}
func envelope(identifier uint32, kind byte, payload []byte) []byte {
	length := len(payload)
	if length > 15 {
		return nil
	}
	encoded := make([]byte, envelopeSize+len(payload))
	binary.BigEndian.PutUint32(encoded, identifier)
	encoded[4] = kind | byte(length)*4
	copy(encoded[5:], payload)
	return encoded
}

func (listener *listenerAdapter) run() {
	defer close(listener.done)
	stopping := make(chan struct{})
	type transmission struct {
		peer    string
		payload []byte
		reply   chan error
	}
	outgoing := make(chan transmission, 32)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case request := <-outgoing:
				err := listener.peripheral.Send(request.peer, request.payload, stopping)
				if request.reply != nil {
					request.reply <- err
				}
			case <-stopping:
				return
			}
		}
	}()
	defer func() { close(stopping); _ = listener.peripheral.Close(); <-writerDone }()
	queue := func(peer string, payload []byte, reply chan error) bool {
		select {
		case outgoing <- transmission{peer: peer, payload: payload, reply: reply}:
			return true
		default:
			if reply != nil {
				reply <- transport.ErrUnavailable
			}
			return false
		}
	}
	links := make(map[string]*linkAdapter)
	defer func() {
		for _, link := range links {
			close(link.done)
		}
	}()
	var pending []net.Conn
	var lastMetadataResponse time.Time
	for {
		var accepted chan net.Conn
		var next net.Conn
		if len(pending) > 0 {
			accepted = listener.accepted
			next = pending[0]
		}
		select {
		case accepted <- next:
			pending = pending[1:]
		case received, open := <-listener.peripheral.Packets():
			if !open {
				return
			}
			if len(received.Payload) < envelopeSize || len(received.Payload) > 20 {
				continue
			}
			identifier := binary.BigEndian.Uint32(received.Payload[:4])
			if identifier == 0 {
				continue
			}
			key := received.Peer + fmt.Sprintf("/%08x", identifier)
			link := links[key]
			length := envelopeSize + int(received.Payload[4]>>2)
			if length > len(received.Payload) {
				continue
			}
			received.Payload = received.Payload[:length]
			switch received.Payload[4] & 3 {
			case messageOpen:
				// Some board SDKs cannot perform long GATT reads. Bounded public
				// metadata replies use the minimum MTU without opening a session.
				if len(received.Payload) == envelopeSize+1 && received.Payload[envelopeSize] == metadataMarker {
					if time.Since(lastMetadataResponse) < time.Second {
						continue
					}
					lastMetadataResponse = time.Now()
					for offset := 0; offset < len(listener.metadata); offset += metadataPartSize {
						end := offset + metadataPartSize
						if end > len(listener.metadata) {
							end = len(listener.metadata)
						}
						payload := append([]byte{metadataMarker, byte((offset / metadataPartSize) & 15)}, listener.metadata[offset:end]...)
						if !queue(received.Peer, envelope(identifier, messageReady, payload), nil) {
							break
						}
					}
					continue
				}
				if len(received.Payload) != envelopeSize {
					continue
				}
				if link == nil {
					if len(links) >= 8 || len(pending) >= 4 {
						continue
					}
					// Notifications can be broadcast by the native stack, so IDs
					// must be unique across native peers as well as within one peer.
					collision := false
					for _, existing := range links {
						if existing.identifier == identifier {
							collision = true
							break
						}
					}
					if collision {
						continue
					}
					link = &linkAdapter{peer: received.Peer, identifier: identifier, incoming: make(chan []byte, 16), done: make(chan struct{}), commands: listener.commands, ownerDone: listener.done, local: listener.endpoint, remote: transport.Endpoint{Scheme: "ble", Host: received.Peer, Service: listener.endpoint.Service}}
					stream, err := packet.Open(link)
					if err != nil {
						continue
					}
					links[key] = link
					pending = append(pending, stream)
				}
				if !queue(received.Peer, envelope(identifier, messageReady, nil), nil) {
					close(link.done)
					delete(links, key)
				}
			case messageData:
				if link != nil {
					select {
					case link.incoming <- append([]byte(nil), received.Payload[envelopeSize:]...):
					default:
						close(link.done)
						delete(links, key)
					}
				}
			case messageClose:
				if link != nil {
					close(link.done)
					delete(links, key)
				}
			}
		case command := <-listener.commands:
			switch request := command.(type) {
			case closeListener:
				return
			case sendPacket:
				key := request.link.peer + fmt.Sprintf("/%08x", request.link.identifier)
				if links[key] != request.link {
					request.reply <- net.ErrClosed
					continue
				}
				if !queue(request.link.peer, envelope(request.link.identifier, messageData, request.payload), request.reply) {
					close(request.link.done)
					delete(links, key)
				}
			case closeLink:
				key := request.link.peer + fmt.Sprintf("/%08x", request.link.identifier)
				if links[key] == request.link {
					delete(links, key)
					close(request.link.done)
					queue(request.link.peer, envelope(request.link.identifier, messageClose, nil), nil)
				}
				request.reply <- struct{}{}
			}
		case <-listener.options.Done:
			return
		}
	}
}

func (driver Driver) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
	if err := transport.CheckSecurity(options); err != nil {
		return nil, err
	}
	if endpoint.Host == "" {
		return nil, errors.New("netchan: BLE Dial requires a device identifier")
	}
	lifetime := transport.NewLifetime(options)
	failed := true
	defer func() {
		if failed {
			lifetime.Close()
		}
	}()
	radio, err := driver.radio(lifetime.Done)
	if err != nil {
		return nil, err
	}
	central, err := radio.Dial(endpoint.Host, UUID(endpoint.Service, 0), lifetime.Done)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", transport.ErrUnavailable, err)
	}
	var identifierBytes [4]byte
	if _, err = rand.Read(identifierBytes[:]); err != nil {
		_ = central.Close()
		return nil, err
	}
	identifier := binary.BigEndian.Uint32(identifierBytes[:])
	if identifier == 0 {
		identifier = 1
	}
	done := make(chan struct{})
	link := &linkAdapter{peer: endpoint.Host, identifier: identifier, incoming: make(chan []byte, 16), done: make(chan struct{}), commands: make(chan any), ownerDone: done, local: transport.Endpoint{Scheme: "ble", Host: "local", Service: endpoint.Service}, remote: endpoint}
	ready := make(chan error, 1)
	go func() { defer lifetime.Close(); runCentral(central, link, lifetime.Done, done, ready) }()
	if err = <-ready; err != nil {
		return nil, err
	}
	stream, err := packet.Open(link)
	if err != nil {
		_ = link.Close()
		return nil, err
	}
	options.TLS = transport.ClientTLS(options.TLS, endpoint, options.PeerID)
	options.Done = lifetime.Done
	connection, err := transport.SecureClient(stream, options)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err = lifetime.Promote(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	failed = false
	return connection, nil
}

func runCentral(central Central, link *linkAdapter, ownerDone <-chan struct{}, done chan struct{}, ready chan error) {
	defer close(done)
	defer close(link.done)
	defer central.Close()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	opened := false
	defer func() {
		if !opened {
			ready <- transport.ErrUnavailable
		}
	}()
	if err := central.Send(envelope(link.identifier, messageOpen, nil), ownerDone); err != nil {
		return
	}
	for {
		select {
		case payload, open := <-central.Packets():
			if !open {
				return
			}
			if len(payload) < envelopeSize || binary.BigEndian.Uint32(payload[:4]) != link.identifier {
				continue
			}
			length := envelopeSize + int(payload[4]>>2)
			if length > len(payload) {
				continue
			}
			payload = payload[:length]
			switch payload[4] & 3 {
			case messageReady:
				if len(payload) != envelopeSize {
					continue
				}
				if !opened {
					opened = true
					ready <- nil
					deadline.Stop()
				}
			case messageData:
				select {
				case link.incoming <- append([]byte(nil), payload[5:]...):
				default:
					return
				}
			case messageClose:
				return
			}
		case command := <-link.commands:
			switch request := command.(type) {
			case sendPacket:
				request.reply <- central.Send(envelope(link.identifier, messageData, request.payload), request.done)
			case closeLink:
				_ = central.Send(envelope(link.identifier, messageClose, nil), ownerDone)
				request.reply <- struct{}{}
				return
			}
		case <-ticker.C:
			if !opened {
				if err := central.Send(envelope(link.identifier, messageOpen, nil), ownerDone); err != nil {
					return
				}
			}
		case <-deadline.C:
			if !opened {
				return
			}
		case <-ownerDone:
			return
		}
	}
}

func (driver Driver) Discover(service string, results chan<- transport.Candidate, done <-chan struct{}) error {
	radio, err := driver.radio(done)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for {
		devices, scanErr := scanDevices(radio, UUID(service, 0), done)
		if scanErr != nil {
			return scanErr
		}
		for _, device := range devices {
			select {
			case <-done:
				return nil
			default:
			}
			if seen[device] || len(seen) >= 32 {
				continue
			}
			seen[device] = true
			central, dialErr := radio.Dial(device, UUID(service, 0), done)
			if dialErr != nil {
				continue
			}
			metadata := central.Metadata()
			if _, _, _, decodeErr := DecodeMetadata(metadata); decodeErr != nil {
				metadata, _ = readPeerMetadata(central, done)
			}
			remoteService, peerID, instanceID, decodeErr := DecodeMetadata(metadata)
			_ = central.Close()
			if decodeErr != nil || remoteService != service {
				continue
			}
			candidate := transport.Candidate{Endpoint: transport.Endpoint{Scheme: "ble", Host: device, Service: service}, PeerID: peerID, InstanceID: instanceID, Expires: time.Now().Add(30 * time.Second)}
			select {
			case results <- candidate:
			case <-done:
				return nil
			}
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
}

// Discovery gives the adapter back before connecting. Controllers with one
// native session cannot scan and inspect a peripheral at the same time.
func scanDevices(radio Radio, serviceUUID string, done <-chan struct{}) ([]string, error) {
	devices := make(chan string, 16)
	canceled := make(chan struct{})
	finished := make(chan error, 1)
	go func() { finished <- radio.Scan(serviceUUID, devices, canceled) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	timeout := timer.C
	ownerDone := done
	stopped := false
	stop := func() {
		if !stopped {
			close(canceled)
			stopped = true
		}
		timeout, ownerDone = nil, nil
	}
	defer stop()
	var found []string
	add := func(device string) {
		if len(found) >= 32 {
			return
		}
		for _, known := range found {
			if known == device {
				return
			}
		}
		found = append(found, device)
	}
	for {
		select {
		case device := <-devices:
			add(device)
		case err := <-finished:
			for {
				select {
				case device := <-devices:
					add(device)
				default:
					return found, err
				}
			}
		case <-timeout:
			stop()
		case <-ownerDone:
			stop()
		}
	}
}
