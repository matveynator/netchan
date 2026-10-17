//go:build darwin && (tinygo || (!amd64 && !arm64))

package ble

import "github.com/matveynator/netchan/v2/pkg/transport"

func systemRadio(done <-chan struct{}) (Radio, error) { return nil, transport.ErrUnsupported }
