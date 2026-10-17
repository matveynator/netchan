//go:build !netchan_no_ble

package netchan

import (
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/ble"
)

func appendBluetoothDrivers(drivers []transport.Driver, done <-chan struct{}) []transport.Driver {
	if driver, err := ble.NewDriver(done); err == nil {
		drivers = append(drivers, driver)
	}
	return drivers
}
