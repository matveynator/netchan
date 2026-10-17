package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
)

func TestParseTransportAddresses(t *testing.T) {
	for _, example := range []struct{ address, canonical, scheme string }{
		{"127.0.0.1:0", "127.0.0.1:0", "tcp"},
		{"tcp://[::1]:123", "tcp://[::1]:123", "tcp"},
		{"udp://localhost:123", "quic://localhost:123", "quic"},
		{"ble://aabbccddeeff/tasks", "ble://aabbccddeeff/tasks", "ble"},
		{"rfcomm://AA:BB:CC:DD:EE:FF/tasks", "rfcomm://AA:BB:CC:DD:EE:FF/tasks", "rfcomm"},
		{"nearby:///tasks", "nearby:///tasks", "nearby"},
	} {
		t.Run(example.address, func(t *testing.T) {
			endpoint, err := Parse(example.address)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.String() != example.canonical || endpoint.Scheme != example.scheme {
				t.Fatalf("parsed %s as %#v", example.address, endpoint)
			}
		})
	}
	for _, address := range []string{"udp://localhost", "tcp://localhost:123/path", "ble://device", "ble:///", "ble:///a%2fb", "ble:///tasks?secret=1", "tcp://name@host:1"} {
		if _, err := Parse(address); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	if _, err := Parse("unknown:///tasks"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported scheme: %v", err)
	}
}

func TestDiscoveryPinPreservesTLSVerification(t *testing.T) {
	called := make(chan struct{}, 1)
	policyError := errors.New("application verification rejected peer")
	configuration := &tls.Config{MinVersion: tls.VersionTLS12, VerifyConnection: func(tls.ConnectionState) error { called <- struct{}{}; return policyError }}
	key := []byte("selected public key")
	secured := ClientTLS(configuration, Endpoint{Scheme: "tcp", Host: "localhost:123"}, PublicKeyID(key))
	if secured.InsecureSkipVerify || secured.MinVersion != tls.VersionTLS13 || secured.ServerName != "localhost" {
		t.Fatal("discovery relaxed TLS policy")
	}
	if configuration.MinVersion != tls.VersionTLS12 || configuration.ServerName != "" {
		t.Fatal("caller TLS configuration was mutated")
	}
	if err := secured.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: []byte("other key")}}}); err == nil {
		t.Fatal("wrong public key passed discovery pin")
	}
	select {
	case <-called:
		t.Fatal("application verifier ran before the discovery pin")
	default:
	}
	err := secured.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: key}}})
	if !errors.Is(err, policyError) {
		t.Fatalf("application policy was replaced: %v", err)
	}
}

func FuzzTransportAddress(f *testing.F) {
	for _, address := range []string{"localhost:1", "quic://[::1]:123", "ble://AA:BB:CC:DD:EE:FF/tasks", "nearby:///tasks", "rfcomm:///a%252fb"} {
		f.Add(address)
	}
	f.Fuzz(func(t *testing.T, address string) {
		if len(address) > 8192 {
			return
		}
		endpoint, err := Parse(address)
		if err != nil {
			return
		}
		reparsed, err := Parse(endpoint.String())
		if err != nil || reparsed != endpoint {
			t.Fatalf("address does not round trip: %#v -> %#v, %v", endpoint, reparsed, err)
		}
	})
}
