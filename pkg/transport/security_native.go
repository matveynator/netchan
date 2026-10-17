//go:build !tinygo

package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"time"
)

func CheckSecurity(options Options) error {
	if options.TLS == nil {
		return errors.New("netchan: TLS configuration is required")
	}
	return nil
}

func defaultSecureClient(connection net.Conn, options Options) (net.Conn, error) {
	configuration := options.TLS.Clone()
	if configuration.MinVersion < tls.VersionTLS13 {
		configuration.MinVersion = tls.VersionTLS13
	}
	secured := tls.Client(connection, configuration)
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-options.Done:
			_ = secured.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watcherDone }()
	if err := secured.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		_ = secured.Close()
		return nil, err
	}
	if err := secured.Handshake(); err != nil {
		_ = secured.Close()
		return nil, err
	}
	if err := secured.SetDeadline(time.Time{}); err != nil {
		_ = secured.Close()
		return nil, err
	}
	return secured, nil
}

func defaultSecureServer(connection net.Conn, options Options) (net.Conn, error) {
	configuration := options.TLS.Clone()
	if configuration.MinVersion < tls.VersionTLS13 {
		configuration.MinVersion = tls.VersionTLS13
	}
	return tls.Server(connection, configuration), nil
}
