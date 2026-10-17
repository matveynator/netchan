//go:build tinygo

package transport

import (
	"fmt"
	"net"
)

// TinyGo's crypto/tls.Client and Server wrap the raw connection without a
// software record layer. Refusing that fallback prevents plaintext BLE/TCP.
func CheckSecurity(options Options) error {
	if options.Security == nil {
		return fmt.Errorf("%w: target requires a verified stream TLS Security backend", ErrUnavailable)
	}
	return nil
}

func defaultSecureClient(connection net.Conn, options Options) (net.Conn, error) {
	_ = connection.Close()
	return nil, CheckSecurity(options)
}
func defaultSecureServer(connection net.Conn, options Options) (net.Conn, error) {
	_ = connection.Close()
	return nil, CheckSecurity(options)
}
