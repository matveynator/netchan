//go:build !tinygo && !netchan_no_rfcomm

package netchan

import (
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/rfcomm"
)

func appendRFCOMMDriver(drivers []transport.Driver) []transport.Driver {
	return append(drivers, rfcomm.Driver{})
}
