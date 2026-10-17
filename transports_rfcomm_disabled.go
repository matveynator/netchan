//go:build tinygo || netchan_no_rfcomm

package netchan

import "github.com/matveynator/netchan/v2/pkg/transport"

func appendRFCOMMDriver(drivers []transport.Driver) []transport.Driver { return drivers }
