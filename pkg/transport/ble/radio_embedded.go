//go:build tinygo && baremetal && (softdevice || hci || ninafw || cyw43439 || (espradio && (esp32c3 || esp32s3))) && (!espradio || esp32c3 || esp32s3)

package ble

import "tinygo.org/x/bluetooth"

func systemRadio(done <-chan struct{}) (Radio, error) {
	return NewSDKRadio(bluetooth.DefaultAdapter), nil
}
