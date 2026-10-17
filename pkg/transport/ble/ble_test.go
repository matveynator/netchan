package ble_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	netchan "github.com/matveynator/netchan/v2"
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/ble"
)

type fakeRadio struct {
	commands chan any
	done     chan struct{}
}
type fakeListen struct {
	uuid     string
	metadata []byte
	reply    chan *fakePeripheral
}
type fakeDial struct {
	uuid  string
	reply chan *fakeCentral
}
type fakeScan struct {
	active bool
	reply  chan struct{}
}
type fakeSend struct {
	peer    string
	payload []byte
	central bool
	reply   chan error
}
type fakeClose struct {
	peer    string
	central bool
	reply   chan struct{}
}
type fakePeripheral struct {
	radio    *fakeRadio
	incoming chan ble.Received
	done     chan struct{}
}
type fakeCentral struct {
	radio    *fakeRadio
	incoming chan []byte
	done     chan struct{}
	peer     string
	metadata []byte
}

func newFakeRadio(t *testing.T) *fakeRadio {
	radio := &fakeRadio{commands: make(chan any), done: make(chan struct{})}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); <-radio.done })
	go func() {
		defer close(radio.done)
		var peripheral *fakePeripheral
		var service string
		var metadata []byte
		clients := make(map[string]*fakeCentral)
		count := 0
		clientCount := 0
		scanning := false
		for {
			select {
			case command := <-radio.commands:
				switch request := command.(type) {
				case fakeListen:
					peripheral = &fakePeripheral{radio: radio, incoming: make(chan ble.Received, 256), done: make(chan struct{})}
					service = request.uuid
					metadata = append([]byte(nil), request.metadata...)
					request.reply <- peripheral
				case fakeScan:
					scanning = request.active
					close(request.reply)
				case fakeDial:
					if scanning || peripheral == nil || request.uuid != service {
						request.reply <- nil
						continue
					}
					clientCount++
					peer := fmt.Sprintf("client-%d", clientCount)
					central := &fakeCentral{radio: radio, incoming: make(chan []byte, 256), done: make(chan struct{}), peer: peer, metadata: append([]byte(nil), metadata...)}
					clients[peer] = central
					request.reply <- central
				case fakeSend:
					count++
					if count%37 == 0 {
						request.reply <- nil
						continue
					}
					if request.central {
						if peripheral == nil {
							request.reply <- net.ErrClosed
							continue
						}
						peripheral.incoming <- ble.Received{Peer: request.peer, Payload: append([]byte(nil), request.payload...)}
					} else {
						central := clients[request.peer]
						if central == nil {
							request.reply <- net.ErrClosed
							continue
						}
						// Some embedded GATT stacks have fixed-size characteristic values.
						padded := make([]byte, 20)
						copy(padded, request.payload)
						central.incoming <- padded
					}
					request.reply <- nil
				case fakeClose:
					if request.central {
						if central := clients[request.peer]; central != nil {
							close(central.done)
							close(central.incoming)
							delete(clients, request.peer)
						}
					} else if peripheral != nil {
						close(peripheral.done)
						close(peripheral.incoming)
						peripheral = nil
					}
					close(request.reply)
				}
			case <-stop:
				return
			}
		}
	}()
	return radio
}
func (radio *fakeRadio) Listen(uuid string, metadata []byte, done <-chan struct{}) (ble.Peripheral, error) {
	reply := make(chan *fakePeripheral, 1)
	radio.commands <- fakeListen{uuid: uuid, metadata: metadata, reply: reply}
	return <-reply, nil
}
func (radio *fakeRadio) Dial(device, uuid string, done <-chan struct{}) (ble.Central, error) {
	reply := make(chan *fakeCentral, 1)
	radio.commands <- fakeDial{uuid: uuid, reply: reply}
	central := <-reply
	if central == nil {
		return nil, transport.ErrUnavailable
	}
	return central, nil
}
func (radio *fakeRadio) Scan(_ string, devices chan<- string, done <-chan struct{}) error {
	change := func(active bool) {
		reply := make(chan struct{})
		radio.commands <- fakeScan{active: active, reply: reply}
		<-reply
	}
	change(true)
	defer change(false)
	select {
	case devices <- "fake-radio":
	case <-done:
		return nil
	}
	<-done
	return nil
}
func (radio *fakeRadio) send(peer string, payload []byte, central bool, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case radio.commands <- fakeSend{peer: peer, payload: append([]byte(nil), payload...), central: central, reply: reply}:
	case <-done:
		return net.ErrClosed
	case <-radio.done:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-done:
		return net.ErrClosed
	case <-radio.done:
		return net.ErrClosed
	}
}
func (radio *fakeRadio) close(peer string, central bool, done <-chan struct{}) error {
	reply := make(chan struct{})
	select {
	case radio.commands <- fakeClose{peer: peer, central: central, reply: reply}:
	case <-done:
		return nil
	case <-radio.done:
		return nil
	}
	<-reply
	return nil
}
func (peripheral *fakePeripheral) Packets() <-chan ble.Received { return peripheral.incoming }
func (*fakePeripheral) Address() string                         { return "fake-radio" }
func (peripheral *fakePeripheral) Close() error {
	return peripheral.radio.close("", false, peripheral.done)
}
func (peripheral *fakePeripheral) Send(peer string, payload []byte, done <-chan struct{}) error {
	return peripheral.radio.send(peer, payload, false, done)
}
func (central *fakeCentral) Packets() <-chan []byte { return central.incoming }
func (central *fakeCentral) Metadata() []byte {
	return append([]byte(nil), central.metadata[:min(20, len(central.metadata))]...)
}
func (central *fakeCentral) Address() string { return "fake-radio" }
func (central *fakeCentral) Close() error {
	return central.radio.close(central.peer, true, central.done)
}
func (central *fakeCentral) Send(payload []byte, done <-chan struct{}) error {
	return central.radio.send(central.peer, payload, true, done)
}

func tlsConfigurations(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "netchan.test"}, DNSNames: []string{"netchan.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	encoded, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{encoded}, PrivateKey: key, Leaf: parsed}}}, &tls.Config{RootCAs: roots, ServerName: "netchan.test"}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case result, open := <-channel:
		if !open {
			t.Fatal("channel closed")
		}
		return result
	case <-time.After(20 * time.Second):
		t.Fatal("channel deadline")
		var zero T
		return zero
	}
}

func TestBLEDuplexTLSWithNestedChannelsAndPacketLoss(t *testing.T) {
	type task struct {
		Text  string
		Reply chan<- string
		Stop  <-chan struct{}
	}
	radio := newFakeRadio(t)
	serverTLS, clientTLS := tlsConfigurations(t)
	driver := ble.Driver{Radio: radio}
	listener, err := netchan.Listen[task]("ble:///tasks", netchan.Config{TLS: serverTLS, Transports: []transport.Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := netchan.Dial[task](listener.Address, netchan.Config{TLS: clientTLS, Transports: []transport.Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Abort() })
	server := receive(t, listener.Channels)
	reply := make(chan string)
	stop := make(chan struct{})
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(task{Text: "over BLE", Reply: reply, Stop: stop}) }()
	received := receive(t, server.Receive)
	if received.Text != "over BLE" {
		t.Fatal(received.Text)
	}
	if err := receive(t, delivered); err != nil {
		t.Fatal(err)
	}
	go func() { received.Reply <- "answer" }()
	if answer := receive(t, reply); answer != "answer" {
		t.Fatal(answer)
	}
	close(stop)
	select {
	case <-received.Stop:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not arrive")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("peer did not close")
	}
}

func TestBLERejectsUntrustedTLS(t *testing.T) {
	radio := newFakeRadio(t)
	serverTLS, clientTLS := tlsConfigurations(t)
	clientTLS.RootCAs = x509.NewCertPool()
	driver := ble.Driver{Radio: radio}
	listener, err := netchan.Listen[string]("ble:///tasks", netchan.Config{TLS: serverTLS, Transports: []transport.Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := netchan.Dial[string](listener.Address, netchan.Config{TLS: clientTLS, Transports: []transport.Driver{driver}})
	if err == nil {
		_ = client.Abort()
		t.Fatal("untrusted BLE peer accepted")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("TLS error: %v", err)
	}
}

func FuzzMetadata(f *testing.F) {
	encoded, _ := ble.EncodeMetadata("tasks", fmt.Sprintf("%064x", 1), fmt.Sprintf("%032x", 2))
	f.Add(encoded)
	f.Fuzz(func(t *testing.T, payload []byte) { _, _, _, _ = ble.DecodeMetadata(payload) })
}

func TestDiscoveryWithExclusiveRadioAndTruncatedGATTRead(t *testing.T) {
	radio := newFakeRadio(t)
	serverTLS, _ := tlsConfigurations(t)
	driver := ble.Driver{Radio: radio}
	listener, err := netchan.Listen[string]("ble:///tasks", netchan.Config{TLS: serverTLS, Transports: []transport.Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	results := make(chan transport.Candidate, 1)
	finished := make(chan error, 1)
	go func() { finished <- driver.Discover("tasks", results, done) }()
	defer func() { close(done); <-finished }()
	candidate := receive(t, results)
	peer, err := transport.PeerID(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.PeerID != peer || len(candidate.InstanceID) != 32 || candidate.Endpoint.Service != "tasks" {
		t.Fatalf("discovery = %#v", candidate)
	}
}
