package nearby

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

type discoveryFixture struct {
	candidates []transport.Candidate
	attempts   chan string
	stopped    chan struct{}
	fail       bool
}

func (discoveryFixture) Scheme() string { return "ble" }
func (discoveryFixture) Listen(transport.Endpoint, transport.Options) (net.Listener, error) {
	return nil, transport.ErrUnsupported
}
func (fixture discoveryFixture) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
	fixture.attempts <- endpoint.Host
	if fixture.fail {
		return nil, errors.New("fixture rejected peer certificate")
	}
	client, server := net.Pipe()
	go func() { <-options.OwnerDone; _ = server.Close() }()
	return client, nil
}
func (fixture discoveryFixture) Discover(service string, candidates chan<- transport.Candidate, done <-chan struct{}) error {
	defer close(fixture.stopped)
	for _, candidate := range fixture.candidates {
		select {
		case candidates <- candidate:
		case <-done:
			return nil
		}
	}
	<-done
	return nil
}
func candidate(device, peer, instance string) transport.Candidate {
	return transport.Candidate{Endpoint: transport.Endpoint{Scheme: "ble", Host: device, Service: "tasks"}, PeerID: strings.Repeat(peer, 64), InstanceID: strings.Repeat(instance, 32), Expires: time.Now().Add(time.Minute)}
}

func TestNearbyPinsNodeAndJoinsDiscovery(t *testing.T) {
	ownerDone := make(chan struct{})
	defer close(ownerDone)
	fixture := discoveryFixture{candidates: []transport.Candidate{candidate("first", "a", "b")}, attempts: make(chan string, 8), stopped: make(chan struct{})}
	driver := Driver{Drivers: []transport.Driver{fixture}}
	connection, err := driver.Dial(transport.Endpoint{Scheme: "nearby", Service: "tasks"}, transport.Options{Done: ownerDone})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	select {
	case <-fixture.stopped:
	default:
		t.Fatal("discovery still owns callbacks after Dial returned")
	}
	address := connection.(*resolvedConnection).ReconnectAddress()
	if address != "nearby://"+strings.Repeat("a", 64)+"~"+strings.Repeat("b", 32)+"/tasks" {
		t.Fatalf("reconnect address = %s", address)
	}
	if got := <-fixture.attempts; got != "first" {
		t.Fatalf("attempt = %s", got)
	}
}
func TestNearbyRejectsAmbiguityBeforeSendingHello(t *testing.T) {
	ownerDone := make(chan struct{})
	defer close(ownerDone)
	fixture := discoveryFixture{candidates: []transport.Candidate{candidate("one", "a", "b"), candidate("two", "c", "d")}, attempts: make(chan string, 8), stopped: make(chan struct{})}
	_, err := (Driver{Drivers: []transport.Driver{fixture}}).Dial(transport.Endpoint{Scheme: "nearby", Service: "tasks"}, transport.Options{Done: ownerDone})
	if !errors.Is(err, transport.ErrAmbiguousPeer) {
		t.Fatalf("Dial = %v", err)
	}
	select {
	case attempt := <-fixture.attempts:
		t.Fatalf("ambiguous discovery opened %s", attempt)
	default:
	}
}
func TestNearbyReconnectFiltersInstanceAndExpiredRecords(t *testing.T) {
	ownerDone := make(chan struct{})
	defer close(ownerDone)
	expired := candidate("expired", "a", "b")
	expired.Expires = time.Now().Add(-time.Second)
	fixture := discoveryFixture{candidates: []transport.Candidate{expired, candidate("other-instance", "a", "c"), candidate("resumed", "a", "b")}, attempts: make(chan string, 8), stopped: make(chan struct{})}
	connection, err := (Driver{Drivers: []transport.Driver{fixture}}).Dial(transport.Endpoint{Scheme: "nearby", Host: strings.Repeat("a", 64) + "~" + strings.Repeat("b", 32), Service: "tasks"}, transport.Options{Done: ownerDone})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if got := <-fixture.attempts; got != "resumed" {
		t.Fatalf("attempt = %s", got)
	}
}

func TestNearbyCancellationJoinsDiscovery(t *testing.T) {
	ownerDone := make(chan struct{})
	fixture := discoveryFixture{attempts: make(chan string, 8), stopped: make(chan struct{})}
	finished := make(chan error, 1)
	go func() {
		_, err := (Driver{Drivers: []transport.Driver{fixture}}).Dial(transport.Endpoint{Scheme: "nearby", Service: "tasks"}, transport.Options{Done: ownerDone})
		finished <- err
	}()
	close(ownerDone)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled dial succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled discovery did not exit")
	}
	select {
	case <-fixture.stopped:
	default:
		t.Fatal("discovery not joined")
	}
}
