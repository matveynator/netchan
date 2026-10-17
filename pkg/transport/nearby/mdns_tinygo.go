//go:build tinygo

package nearby

import (
	"github.com/matveynator/netchan/v2/pkg/transport"
	"net"
)

// Only this DNS-SD backend needs unsupported OS socket controls. Board drivers
// can still provide their own discovery alongside BLE and explicit endpoints.
func announceIP(net.Addr, string, transport.Options) (func(), error) {
	return nil, transport.ErrUnsupported
}
func discoverIP(string, chan<- transport.Candidate, <-chan struct{}) error {
	return transport.ErrUnsupported
}
