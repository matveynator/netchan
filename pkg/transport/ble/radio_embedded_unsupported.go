//go:build tinygo && baremetal && !((softdevice || hci || ninafw || cyw43439 || (espradio && (esp32c3 || esp32s3))) && (!espradio || esp32c3 || esp32s3))

package ble

import "github.com/matveynator/netchan/v2/pkg/transport"

func systemRadio(done <-chan struct{}) (Radio, error) { return nil, transport.ErrUnsupported }
