package netchan

import "github.com/matveynator/netchan/v2/pkg/transport"

func defaultTransportDrivers(done <-chan struct{}) []transport.Driver {
	var drivers []transport.Driver
	drivers = appendTCPDriver(drivers)
	drivers = appendQUICDriver(drivers)
	drivers = appendRFCOMMDriver(drivers)
	drivers = appendBluetoothDrivers(drivers, done)
	return drivers
}
