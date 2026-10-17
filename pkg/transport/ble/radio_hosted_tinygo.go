//go:build tinygo && !baremetal && (linux || windows)

package ble

import "github.com/matveynator/netchan/v2/pkg/transport"

// Hosted TinyGo lacks the Unix ancillary-data and native WinRT APIs needed by
// these OS adapters. The core, TCP and caller-supplied radios remain available.
func systemRadio(done <-chan struct{}) (Radio, error) { return nil, transport.ErrUnsupported }
