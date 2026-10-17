//go:build (windows && !tinygo) || (tinygo && baremetal && (softdevice || hci || ninafw || cyw43439 || (espradio && (esp32c3 || esp32s3))) && (!espradio || esp32c3 || esp32s3))

package ble

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
	"tinygo.org/x/bluetooth"
)

// An adapter descriptor moves through a channel. Embedded SDKs expose one
// physical radio; its right of use belongs to one scan or connection at a time.
type sdkAdapter struct {
	adapter *bluetooth.Adapter
	enabled bool
}
type SDKRadio struct{ adapters chan sdkAdapter }

func NewSDKRadio(adapter *bluetooth.Adapter) *SDKRadio {
	ownership := make(chan sdkAdapter, 1)
	ownership <- sdkAdapter{adapter: adapter}
	return &SDKRadio{adapters: ownership}
}
func (radio *SDKRadio) take(done <-chan struct{}) (sdkAdapter, error) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case owned := <-radio.adapters:
		if !owned.enabled {
			owned.adapter.SetConnectHandler(func(bluetooth.Device, bool) {})
			if err := enableSDKAdapter(owned.adapter); err != nil {
				radio.adapters <- owned
				return sdkAdapter{}, fmt.Errorf("%w: %w", transport.ErrUnavailable, err)
			}
			owned.enabled = true
		}
		return owned, nil
	case <-done:
		return sdkAdapter{}, net.ErrClosed
	case <-timer.C:
		return sdkAdapter{}, fmt.Errorf("%w: BLE radio is busy", transport.ErrUnavailable)
	}
}

type sdkPeripheral struct {
	radio         *SDKRadio
	owned         sdkAdapter
	service       *bluetooth.Service
	advertisement *bluetooth.Advertisement
	tx            *bluetooth.Characteristic
	incoming      chan Received
	commands      chan sdkSend
	stop          chan struct{}
	done          chan struct{}
}
type sdkSend struct {
	payload []byte
	reply   chan error
}

func (peripheral *sdkPeripheral) Packets() <-chan Received { return peripheral.incoming }
func (peripheral *sdkPeripheral) Address() string          { return "default" }
func (peripheral *sdkPeripheral) Close() error {
	select {
	case peripheral.stop <- struct{}{}:
	case <-peripheral.done:
	}
	<-peripheral.done
	return nil
}
func (peripheral *sdkPeripheral) Send(peer string, payload []byte, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case peripheral.commands <- sdkSend{payload: append([]byte(nil), payload...), reply: reply}:
	case <-done:
		return net.ErrClosed
	case <-peripheral.done:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-done:
		return net.ErrClosed
	case <-peripheral.done:
		return net.ErrClosed
	}
}

func (radio *SDKRadio) Listen(serviceUUID string, metadata []byte, done <-chan struct{}) (Peripheral, error) {
	leave, err := enterSDKThread()
	if err != nil {
		return nil, err
	}
	defer leave()
	owned, err := radio.take(done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			radio.adapters <- owned
		}
	}()
	uuid, err := bluetooth.ParseUUID(serviceUUID)
	if err != nil {
		return nil, err
	}
	rxUUID, _ := bluetooth.ParseUUID(UUID(serviceUUID, 1))
	txUUID, _ := bluetooth.ParseUUID(UUID(serviceUUID, 2))
	infoUUID, _ := bluetooth.ParseUUID(UUID(serviceUUID, 3))
	peripheral := &sdkPeripheral{radio: radio, owned: owned, incoming: make(chan Received, 32), commands: make(chan sdkSend), stop: make(chan struct{}), done: make(chan struct{}), tx: &bluetooth.Characteristic{}}
	service := &bluetooth.Service{UUID: uuid, Characteristics: []bluetooth.CharacteristicConfig{
		{Handle: &bluetooth.Characteristic{}, UUID: rxUUID, Flags: bluetooth.CharacteristicWritePermission, WriteEvent: func(connection bluetooth.Connection, offset int, payload []byte) {
			if offset != 0 || len(payload) > 20 {
				return
			}
			select {
			case peripheral.incoming <- Received{Peer: fmt.Sprint(uint16(connection)), Payload: append([]byte(nil), payload...)}:
			case <-peripheral.done:
			default:
			}
		}},
		{Handle: peripheral.tx, UUID: txUUID, Flags: bluetooth.CharacteristicNotifyPermission, Value: make([]byte, 20)},
		{Handle: &bluetooth.Characteristic{}, UUID: infoUUID, Flags: bluetooth.CharacteristicReadPermission, Value: append([]byte(nil), metadata[:min(len(metadata), 20)]...)},
	}}
	if err = owned.adapter.AddService(service); err != nil {
		return nil, err
	}
	advertisement := owned.adapter.DefaultAdvertisement()
	if err = advertisement.Configure(bluetooth.AdvertisementOptions{ServiceUUIDs: []bluetooth.UUID{uuid}}); err != nil {
		_ = owned.adapter.RemoveService(service)
		return nil, err
	}
	if err = advertisement.Start(); err != nil {
		_ = owned.adapter.RemoveService(service)
		return nil, err
	}
	peripheral.service, peripheral.advertisement = service, advertisement
	failed = false
	go func() {
		defer close(peripheral.done)
		leave, err := enterSDKThread()
		if err != nil {
			return
		}
		defer leave()
		defer func() { _ = advertisement.Stop(); _ = owned.adapter.RemoveService(service); radio.adapters <- owned }()
		for {
			select {
			case request := <-peripheral.commands:
				_, err := peripheral.tx.Write(request.payload)
				request.reply <- err
			case <-peripheral.stop:
				return
			case <-done:
				return
			}
		}
	}()
	return peripheral, nil
}

type sdkCentral struct {
	radio    *SDKRadio
	owned    sdkAdapter
	device   bluetooth.Device
	write    bluetooth.DeviceCharacteristic
	metadata []byte
	incoming chan []byte
	commands chan sdkSend
	stop     chan struct{}
	done     chan struct{}
	address  string
}

func (central *sdkCentral) Packets() <-chan []byte { return central.incoming }
func (central *sdkCentral) Metadata() []byte       { return append([]byte(nil), central.metadata...) }
func (central *sdkCentral) Address() string        { return central.address }
func (central *sdkCentral) Close() error {
	select {
	case central.stop <- struct{}{}:
	case <-central.done:
	}
	<-central.done
	return nil
}
func (central *sdkCentral) Send(payload []byte, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case central.commands <- sdkSend{payload: append([]byte(nil), payload...), reply: reply}:
	case <-done:
		return net.ErrClosed
	case <-central.done:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-done:
		return net.ErrClosed
	case <-central.done:
		return net.ErrClosed
	}
}

func (radio *SDKRadio) Dial(deviceID, serviceUUID string, done <-chan struct{}) (Central, error) {
	leave, err := enterSDKThread()
	if err != nil {
		return nil, err
	}
	defer leave()
	owned, err := radio.take(done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			radio.adapters <- owned
		}
	}()
	if len(deviceID) == 12 {
		var parts []string
		for index := 0; index < 12; index += 2 {
			parts = append(parts, deviceID[index:index+2])
		}
		deviceID = strings.Join(parts, ":")
	}
	mac, err := bluetooth.ParseMAC(deviceID)
	if err != nil {
		return nil, err
	}
	address := bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: mac}}
	device, err := owned.adapter.Connect(address, bluetooth.ConnectionParams{ConnectionTimeout: bluetooth.NewDuration(10 * time.Second)})
	if err != nil {
		return nil, err
	}
	connected := true
	defer func() {
		if failed && connected {
			_ = device.Disconnect()
		}
	}()
	uuid, _ := bluetooth.ParseUUID(serviceUUID)
	services, err := device.DiscoverServices([]bluetooth.UUID{uuid})
	if err != nil || len(services) != 1 {
		if err == nil {
			err = errors.New("netchan: BLE service missing")
		}
		return nil, err
	}
	characteristics, err := services[0].DiscoverCharacteristics(nil)
	if err != nil {
		return nil, err
	}
	var write, notify bluetooth.DeviceCharacteristic
	matched := 0
	for _, characteristic := range characteristics {
		switch characteristic.UUID().String() {
		case UUID(serviceUUID, 1):
			write = characteristic
			matched |= 1
		case UUID(serviceUUID, 2):
			notify = characteristic
			matched |= 2
		case UUID(serviceUUID, 3):
			matched |= 4
		}
	}
	if matched != 7 {
		return nil, errors.New("netchan: BLE characteristics missing")
	}
	// SDK long-read support varies by controller. Discovery obtains the full
	// record through bounded control packets, independently of ATT read MTU.
	central := &sdkCentral{radio: radio, owned: owned, device: device, write: write, incoming: make(chan []byte, 16), commands: make(chan sdkSend), stop: make(chan struct{}), done: make(chan struct{}), address: deviceID}
	if err = notify.EnableNotifications(func(payload []byte) {
		if len(payload) > 20 {
			return
		}
		select {
		case central.incoming <- append([]byte(nil), payload...):
		case <-central.done:
		default:
		}
	}); err != nil {
		return nil, err
	}
	failed = false
	connected = false
	go func() {
		defer close(central.done)
		leave, err := enterSDKThread()
		if err != nil {
			return
		}
		defer leave()
		defer func() { _ = device.Disconnect(); radio.adapters <- owned }()
		// SDK callbacks may already be in flight at disconnect. The input channel
		// is kept open; the owner broadcasts cancellation through done instead.
		for {
			select {
			case request := <-central.commands:
				_, err := write.Write(request.payload)
				request.reply <- err
			case <-central.stop:
				return
			case <-done:
				return
			}
		}
	}()
	return central, nil
}

func (radio *SDKRadio) Scan(serviceUUID string, devices chan<- string, done <-chan struct{}) error {
	leave, err := enterSDKThread()
	if err != nil {
		return err
	}
	defer leave()
	uuid, err := bluetooth.ParseUUID(serviceUUID)
	if err != nil {
		return err
	}
	for {
		owned, err := radio.take(done)
		if err != nil {
			return nil
		}
		ended := make(chan struct{})
		stopDone := make(chan struct{})
		go func() {
			defer close(stopDone)
			leave, err := enterSDKThread()
			if err != nil {
				return
			}
			defer leave()
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = owned.adapter.StopScan()
			case <-done:
				_ = owned.adapter.StopScan()
			case <-ended:
			}
		}()
		err = owned.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
			if !result.HasServiceUUID(uuid) {
				return
			}
			select {
			case devices <- result.Address.String():
			case <-done:
			default:
			}
		})
		close(ended)
		<-stopDone
		radio.adapters <- owned
		if err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
}
