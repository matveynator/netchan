//go:build !tinygo

package nearby

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/zeroconf/v2"
	"github.com/matveynator/netchan/v2/pkg/transport"
)

func announceIP(address net.Addr, service string, options transport.Options) (func(), error) {
	endpoint, err := transport.Parse(address.String())
	if err != nil {
		return nil, err
	}
	_, portText, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	kind := "_netchan._tcp"
	if endpoint.Scheme == "quic" {
		kind = "_netchan._udp"
	}
	server, err := zeroconf.Register(options.InstanceID+"-"+endpoint.Scheme, kind, "local.", port, []string{"v=2", "service=" + service, "peer=" + options.PeerID, "instance=" + options.InstanceID}, nil)
	if err != nil {
		return nil, err
	}
	return server.Shutdown, nil
}

func discoverIP(service string, results chan<- transport.Candidate, done <-chan struct{}) error {
	finished := make(chan struct{})
	lookupContext, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-done:
			cancel()
		case <-finished:
		}
	}()
	defer func() { close(finished); cancel() }()
	entries := make(chan *zeroconf.ServiceEntry, 32)
	errors := make(chan error, 4)
	for _, kind := range []string{"_netchan._tcp", "_netchan._udp"} {
		for _, family := range []zeroconf.IPType{zeroconf.IPv4, zeroconf.IPv6} {
			go func(kind string, family zeroconf.IPType) {
				ownedEntries := make(chan *zeroconf.ServiceEntry, 16)
				browseDone := make(chan error, 1)
				go func() {
					browseDone <- zeroconf.Browse(lookupContext, kind, "local.", ownedEntries, zeroconf.SelectIPTraffic(family))
				}()
				for {
					select {
					case entry, open := <-ownedEntries:
						if !open {
							errors <- <-browseDone
							return
						}
						select {
						case entries <- entry:
						case <-done:
						}
					case err := <-browseDone:
						errors <- err
						return
					}
				}
			}(kind, family)
		}
	}
	completed := 0
	for completed < 4 {
		select {
		case entry := <-entries:
			properties := make(map[string]string)
			for _, text := range entry.Text {
				key, content, _ := strings.Cut(text, "=")
				properties[key] = content
			}
			if properties["v"] != "2" || properties["service"] != service {
				continue
			}
			scheme := "tcp"
			if strings.Contains(entry.Service, "._udp") {
				scheme = "quic"
			}
			addresses := append(append([]net.IP(nil), entry.AddrIPv4...), entry.AddrIPv6...)
			for _, address := range addresses {
				candidate := transport.Candidate{Endpoint: transport.Endpoint{Scheme: scheme, Host: net.JoinHostPort(address.String(), fmt.Sprint(entry.Port))}, PeerID: properties["peer"], InstanceID: properties["instance"], Expires: entry.Expiry}
				if !candidate.Expires.After(time.Now()) {
					continue
				}
				select {
				case results <- candidate:
				case <-done:
					cancel()
				}
			}
		case <-errors:
			completed++
		case <-done:
			cancel()
		}
	}
	return nil
}
