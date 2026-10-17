//go:build tinygo || netchan_no_quic

package netchan

import "github.com/matveynator/netchan/v2/pkg/transport"

func appendQUICDriver(drivers []transport.Driver) []transport.Driver { return drivers }
