//go:build netchan_no_ble

package netchan

import "github.com/matveynator/netchan/v2/pkg/transport"

func appendBluetoothDrivers(drivers []transport.Driver, done <-chan struct{}) []transport.Driver {
	return drivers
}
