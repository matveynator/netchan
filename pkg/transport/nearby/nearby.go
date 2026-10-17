// Package nearby discovers physical endpoints without owning logical sessions.
package nearby

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

type Driver struct {
	Drivers []transport.Driver
	Report  func(error)
}

func (Driver) Scheme() string { return "nearby" }

func (driver Driver) report(err error) {
	if err != nil && driver.Report != nil {
		driver.Report(err)
	}
}

func (driver Driver) Listen(endpoint transport.Endpoint, options transport.Options) (net.Listener, error) {
	peerID, err := transport.PeerID(options.TLS)
	if err != nil {
		return nil, err
	}
	identifier := make([]byte, 16)
	if _, err = rand.Read(identifier); err != nil {
		return nil, err
	}
	options.PeerID, options.InstanceID = peerID, hex.EncodeToString(identifier)
	var children []net.Listener
	var stopAdvertisements []func()
	seen := make(map[string]bool)
	for _, backend := range driver.Drivers {
		if backend == nil || backend.Scheme() == "nearby" || seen[backend.Scheme()] {
			continue
		}
		seen[backend.Scheme()] = true
		physical := transport.Endpoint{Scheme: backend.Scheme(), Service: endpoint.Service}
		if physical.Scheme == "tcp" || physical.Scheme == "quic" {
			physical.Host = ":0"
			physical.Service = ""
		}
		listener, listenErr := backend.Listen(physical, options)
		if listenErr != nil {
			driver.report(listenErr)
			continue
		}
		if physical.Scheme == "tcp" || physical.Scheme == "quic" {
			stop, advertiseErr := announceIP(listener.Addr(), endpoint.Service, options)
			if advertiseErr != nil {
				_ = listener.Close()
				driver.report(advertiseErr)
				continue
			}
			stopAdvertisements = append(stopAdvertisements, stop)
		}
		children = append(children, listener)
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("%w: no listening transport for nearby service %s", transport.ErrUnavailable, endpoint.Service)
	}
	endpoint.Host = peerID
	listener := &listenerAdapter{endpoint: endpoint, accepted: make(chan acceptResult), stop: make(chan struct{}), done: make(chan struct{}), finished: make(chan struct{})}
	workers := make(chan struct{}, len(children))
	for _, child := range children {
		go func(child net.Listener) {
			defer func() { workers <- struct{}{} }()
			for {
				connection, acceptErr := child.Accept()
				if acceptErr != nil {
					if !errors.Is(acceptErr, net.ErrClosed) {
						driver.report(acceptErr)
					}
					return
				}
				select {
				case listener.accepted <- acceptResult{connection: connection}:
				case <-listener.done:
					_ = connection.Close()
					return
				}
			}
		}(child)
	}
	go func() {
		select {
		case <-listener.stop:
		case <-options.Done:
		}
		close(listener.done)
		for _, stop := range stopAdvertisements {
			stop()
		}
		for _, child := range children {
			_ = child.Close()
		}
		for range children {
			<-workers
		}
		close(listener.finished)
	}()
	return listener, nil
}

type acceptResult struct {
	connection net.Conn
	err        error
}
type listenerAdapter struct {
	endpoint transport.Endpoint
	accepted chan acceptResult
	stop     chan struct{}
	done     chan struct{}
	finished chan struct{}
}

func (listener *listenerAdapter) Addr() net.Addr { return listener.endpoint }
func (listener *listenerAdapter) Accept() (net.Conn, error) {
	select {
	case result := <-listener.accepted:
		return result.connection, result.err
	case <-listener.done:
		return nil, net.ErrClosed
	}
}
func (listener *listenerAdapter) Close() error {
	select {
	case listener.stop <- struct{}{}:
	case <-listener.done:
	}
	<-listener.finished
	return nil
}

type resolvedConnection struct {
	net.Conn
	endpoint transport.Endpoint
}

func (connection *resolvedConnection) ReconnectAddress() string { return connection.endpoint.String() }
func (connection *resolvedConnection) Underlying() net.Conn     { return connection.Conn }

func (driver Driver) Dial(endpoint transport.Endpoint, options transport.Options) (net.Conn, error) {
	if options.OwnerDone == nil {
		options.OwnerDone = options.Done
	}
	wantedPeer, wantedInstance, _ := strings.Cut(endpoint.Host, "~")
	if wantedInstance != "" {
		if decoded, err := hex.DecodeString(wantedInstance); err != nil || len(decoded) != 16 {
			return nil, errors.New("netchan: invalid nearby instance ID")
		}
	}
	if wantedPeer != "" {
		decoded, err := hex.DecodeString(wantedPeer)
		if err != nil || len(decoded) != 32 {
			return nil, errors.New("netchan: nearby peer ID must be a SHA-256 public-key fingerprint")
		}
	}
	finished := make(chan struct{})
	done := make(chan struct{})
	timer := time.NewTimer(60 * time.Second)
	go func() {
		defer close(done)
		select {
		case <-finished:
		case <-options.Done:
		case <-timer.C:
		}
	}()
	candidates := make(chan transport.Candidate, 64)
	workers := make(chan struct{}, len(driver.Drivers)+1)
	workerCount := 0
	attemptCount := 0
	attemptFinished := make(chan struct{}, 256)
	for _, backend := range driver.Drivers {
		if backend != nil && (backend.Scheme() == "tcp" || backend.Scheme() == "quic") {
			workerCount++
			go func() {
				defer func() { workers <- struct{}{} }()
				driver.report(discoverIP(endpoint.Service, candidates, done))
			}()
			break
		}
	}
	seenDrivers := make(map[string]bool)
	for _, backend := range driver.Drivers {
		if backend == nil || seenDrivers[backend.Scheme()] {
			continue
		}
		seenDrivers[backend.Scheme()] = true
		if discoverer, ok := backend.(transport.Discoverer); ok {
			workerCount++
			go func(discoverer transport.Discoverer) {
				defer func() { workers <- struct{}{} }()
				driver.report(discoverer.Discover(endpoint.Service, candidates, done))
			}(discoverer)
		}
	}
	// Discovery jobs own their callback channels until cancellation completes.
	defer func() {
		close(finished)
		timer.Stop()
		<-done
		for index := 0; index < workerCount; index++ {
			<-workers
		}
		for index := 0; index < attemptCount; index++ {
			<-attemptFinished
		}
	}()
	search := time.NewTimer(3 * time.Second)
	defer search.Stop()
	known := make(map[string]transport.Candidate)
	available := make(map[string]bool)
	for _, backend := range driver.Drivers {
		if backend != nil {
			available[backend.Scheme()] = true
		}
	}
searching:
	for {
		select {
		case candidate := <-candidates:
			if !available[candidate.Endpoint.Scheme] {
				continue
			}
			if len(known) >= 256 || candidate.Endpoint.Service != "" && candidate.Endpoint.Service != endpoint.Service || !candidate.Expires.After(time.Now()) {
				continue
			}
			if wantedPeer != "" && candidate.PeerID != wantedPeer || wantedInstance != "" && candidate.InstanceID != wantedInstance {
				continue
			}
			if decoded, err := hex.DecodeString(candidate.PeerID); err != nil || len(decoded) != 32 {
				continue
			}
			if decoded, err := hex.DecodeString(candidate.InstanceID); err != nil || len(decoded) != 16 {
				continue
			}
			known[candidate.Endpoint.String()+"/"+candidate.InstanceID] = candidate
		case <-search.C:
			if len(known) == 0 {
				search.Reset(3 * time.Second)
				continue
			}
			break searching
		case <-done:
			return nil, transport.ErrPeerNotFound
		}
	}
	ordered := make([]transport.Candidate, 0, len(known))
	for _, candidate := range known {
		if candidate.Expires.After(time.Now()) {
			ordered = append(ordered, candidate)
		}
	}
	rank := func(scheme string) int {
		switch scheme {
		case "quic":
			return 0
		case "tcp":
			return 1
		case "rfcomm":
			return 2
		case "ble":
			return 3
		default:
			return 4
		}
	}
	sort.Slice(ordered, func(first, second int) bool {
		return rank(ordered[first].Endpoint.Scheme) < rank(ordered[second].Endpoint.Scheme)
	})
	if len(ordered) == 0 {
		return nil, transport.ErrPeerNotFound
	}
	// Discovery hints cannot authorize a peer. When they name different nodes,
	// refuse to choose one; every accepted connection still verifies TLS trust
	// and the selected public key before the protocol sends HELLO or RESUME.
	identities := make(map[string]struct{})
	for _, candidate := range ordered {
		identities[candidate.PeerID+"/"+candidate.InstanceID] = struct{}{}
	}
	if len(identities) > 1 {
		return nil, transport.ErrAmbiguousPeer
	}
	results := make(chan dialResult)
	limit := make(chan struct{}, 4)
	attempts := 0
	for index, candidate := range ordered {
		var selected transport.Driver
		for _, backend := range driver.Drivers {
			if backend != nil && backend.Scheme() == candidate.Endpoint.Scheme {
				selected = backend
				break
			}
		}
		if selected == nil {
			continue
		}
		attempts++
		attemptCount++
		go func(candidate transport.Candidate, backend transport.Driver, delay time.Duration) {
			defer func() { attemptFinished <- struct{}{} }()
			stagger := time.NewTimer(delay)
			defer stagger.Stop()
			select {
			case <-stagger.C:
			case <-done:
				return
			}
			select {
			case limit <- struct{}{}:
			case <-done:
				return
			}
			defer func() { <-limit }()
			physicalOptions := options
			physicalOptions.Done = done
			physicalOptions.PeerID = candidate.PeerID
			connection, err := backend.Dial(candidate.Endpoint, physicalOptions)
			select {
			case results <- dialResult{connection: connection, candidate: candidate, err: err}:
			case <-done:
				if connection != nil {
					_ = connection.Close()
				}
			}
		}(candidate, selected, time.Duration(index)*250*time.Millisecond)
	}
	var failures []error
	for index := 0; index < attempts; index++ {
		select {
		case result := <-results:
			if result.err != nil {
				if result.connection != nil {
					_ = result.connection.Close()
				}
				failures = append(failures, result.err)
				continue
			}
			if result.connection == nil {
				failures = append(failures, transport.ErrUnavailable)
				continue
			}
			return &resolvedConnection{Conn: result.connection, endpoint: transport.Endpoint{Scheme: "nearby", Host: result.candidate.PeerID + "~" + result.candidate.InstanceID, Service: endpoint.Service}}, nil
		case <-done:
			return nil, fmt.Errorf("%w: discovery connection deadline", transport.ErrUnavailable)
		}
	}
	return nil, fmt.Errorf("%w: %w", transport.ErrPeerNotFound, errors.Join(failures...))
}

type dialResult struct {
	connection net.Conn
	candidate  transport.Candidate
	err        error
}
