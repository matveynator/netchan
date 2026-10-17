//go:build !tinygo && !netchan_no_quic

package netchan

import (
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/quic"
)

func appendQUICDriver(drivers []transport.Driver) []transport.Driver {
	return append(drivers, quic.Driver{})
}
