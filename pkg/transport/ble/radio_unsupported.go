//go:build !linux && !windows && !darwin && !baremetal

package ble

import "github.com/matveynator/netchan/v2/pkg/transport"

func systemRadio(done <-chan struct{}) (Radio, error) { return nil, transport.ErrUnsupported }
