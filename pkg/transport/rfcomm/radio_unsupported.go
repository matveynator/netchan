//go:build (!linux && !windows) || baremetal || tinygo

package rfcomm

import "github.com/matveynator/netchan/v2/pkg/transport"

func systemRadio() (Radio, error) { return nil, transport.ErrUnsupported }
