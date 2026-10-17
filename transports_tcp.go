//go:build !netchan_no_tcp && (!baremetal || netchan_tcp || cyw43439 || espradio || ninafw)

package netchan

import (
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/tcp"
)

func appendTCPDriver(drivers []transport.Driver) []transport.Driver {
	return append(drivers, tcp.Driver{})
}
