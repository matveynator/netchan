//go:build netchan_no_tcp || (baremetal && !netchan_tcp && !cyw43439 && !espradio && !ninafw)

package netchan

import "github.com/matveynator/netchan/v2/pkg/transport"

func appendTCPDriver(drivers []transport.Driver) []transport.Driver { return drivers }
