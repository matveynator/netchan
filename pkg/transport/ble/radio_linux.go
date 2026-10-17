//go:build linux && !baremetal && !tinygo

package ble

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/matveynator/netchan/v2/pkg/transport"
)

type BlueZ struct{ Adapter string }

func systemRadio(done <-chan struct{}) (Radio, error) { return BlueZ{}, nil }

type bluezObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Every D-Bus connection has one owning lifetime. The external D-Bus library
// handles its own socket synchronization; application state stays in actors.
type bluezLifetime struct {
	bus  *dbus.Conn
	stop chan struct{}
	done chan struct{}
}

func ownBluez(bus *dbus.Conn, ownerDone <-chan struct{}) *bluezLifetime {
	lifetime := &bluezLifetime{bus: bus, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		select {
		case <-ownerDone:
		case <-lifetime.stop:
		}
		_ = bus.Close()
		close(lifetime.done)
	}()
	return lifetime
}
func (lifetime *bluezLifetime) Close() error {
	select {
	case lifetime.stop <- struct{}{}:
	case <-lifetime.done:
	}
	<-lifetime.done
	return nil
}

func bluezCall(bus *dbus.Conn, path dbus.ObjectPath, method string, done <-chan struct{}, arguments ...any) *dbus.Call {
	callContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-done:
			cancel()
		case <-finished:
		}
	}()
	call := bus.Object("org.bluez", path).CallWithContext(callContext, method, 0, arguments...)
	close(finished)
	cancel()
	<-watcherDone
	return call
}

func openBluez(adapterName string, done <-chan struct{}) (*bluezLifetime, dbus.ObjectPath, error) {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, "", fmt.Errorf("%w: BlueZ D-Bus: %w", transport.ErrUnavailable, err)
	}
	lifetime := ownBluez(bus, done)
	var objects bluezObjects
	if err = bluezCall(bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", lifetime.done).Store(&objects); err != nil {
		_ = lifetime.Close()
		return nil, "", err
	}
	var adapters []string
	for path, interfaces := range objects {
		if _, exists := interfaces["org.bluez.Adapter1"]; exists {
			if adapterName == "" || strings.TrimPrefix(string(path), "/org/bluez/") == adapterName {
				adapters = append(adapters, string(path))
			}
		}
	}
	if len(adapters) == 0 {
		_ = lifetime.Close()
		return nil, "", fmt.Errorf("%w: no BlueZ adapter", transport.ErrUnavailable)
	}
	sort.Strings(adapters)
	return lifetime, dbus.ObjectPath(adapters[0]), nil
}

type readOnlyProperties struct {
	interfaces map[string]map[string]dbus.Variant
}

func (properties *readOnlyProperties) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if entry, exists := properties.interfaces[iface][property]; exists {
		return entry, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(errors.New("unknown property"))
}
func (properties *readOnlyProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	copy := make(map[string]dbus.Variant)
	for name, entry := range properties.interfaces[iface] {
		copy[name] = entry
	}
	return copy, nil
}
func (*readOnlyProperties) Set(string, string, dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

type objectManager struct{ objects bluezObjects }

func (manager *objectManager) GetManagedObjects() (bluezObjects, *dbus.Error) {
	result := make(bluezObjects)
	for path, interfaces := range manager.objects {
		result[path] = make(map[string]map[string]dbus.Variant)
		for iface, properties := range interfaces {
			result[path][iface] = make(map[string]dbus.Variant)
			for name, entry := range properties {
				result[path][iface][name] = entry
			}
		}
	}
	return result, nil
}

type advertisementObject struct{}

func (*advertisementObject) Release() *dbus.Error { return nil }

type characteristicObject struct {
	metadata []byte
	incoming chan<- Received
	done     <-chan struct{}
}

func (characteristic *characteristicObject) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	offset, _ := options["offset"].Value().(uint16)
	if int(offset) > len(characteristic.metadata) {
		return nil, dbus.NewError("org.bluez.Error.InvalidOffset", nil)
	}
	return append([]byte(nil), characteristic.metadata[offset:]...), nil
}
func (characteristic *characteristicObject) WriteValue(payload []byte, options map[string]dbus.Variant) *dbus.Error {
	peer, valid := options["device"].Value().(dbus.ObjectPath)
	offset, _ := options["offset"].Value().(uint16)
	if !valid || offset != 0 || len(payload) > 20 {
		return dbus.NewError("org.bluez.Error.InvalidArguments", nil)
	}
	select {
	case characteristic.incoming <- Received{Peer: string(peer), Payload: append([]byte(nil), payload...)}:
		return nil
	case <-characteristic.done:
		return dbus.NewError("org.bluez.Error.NotConnected", nil)
	default:
		return dbus.NewError("org.bluez.Error.InProgress", nil)
	}
}
func (*characteristicObject) StartNotify() *dbus.Error { return nil }
func (*characteristicObject) StopNotify() *dbus.Error  { return nil }

type bluezPeripheral struct {
	*bluezLifetime
	incoming     chan Received
	notification dbus.ObjectPath
	address      string
}

func (peripheral *bluezPeripheral) Packets() <-chan Received { return peripheral.incoming }
func (peripheral *bluezPeripheral) Address() string          { return peripheral.address }
func (peripheral *bluezPeripheral) Send(peer string, payload []byte, done <-chan struct{}) error {
	select {
	case <-done:
		return net.ErrClosed
	case <-peripheral.done:
		return net.ErrClosed
	default:
	}
	// BlueZ broadcasts characteristic notifications. The common layer assigns
	// unique link IDs and validates native device identity on incoming writes.
	return peripheral.bus.Emit(peripheral.notification, "org.freedesktop.DBus.Properties.PropertiesChanged", "org.bluez.GattCharacteristic1", map[string]dbus.Variant{"Value": dbus.MakeVariant(payload)}, []string{})
}

func (radio BlueZ) Listen(serviceUUID string, metadata []byte, done <-chan struct{}) (Peripheral, error) {
	lifetime, adapter, err := openBluez(radio.Adapter, done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = lifetime.Close()
		}
	}()
	identifier := make([]byte, 8)
	if _, err = rand.Read(identifier); err != nil {
		return nil, err
	}
	root := dbus.ObjectPath("/org/netchan/b" + hex.EncodeToString(identifier))
	service := dbus.ObjectPath(string(root) + "/service")
	rx, tx, info := dbus.ObjectPath(string(service)+"/rx"), dbus.ObjectPath(string(service)+"/tx"), dbus.ObjectPath(string(service)+"/info")
	incoming := make(chan Received, 64)
	objects := bluezObjects{service: {"org.bluez.GattService1": {"UUID": dbus.MakeVariant(serviceUUID), "Primary": dbus.MakeVariant(true)}}}
	for _, characteristic := range []struct {
		path     dbus.ObjectPath
		uuid     string
		flags    []string
		metadata []byte
	}{{rx, UUID(serviceUUID, 1), []string{"write"}, nil}, {tx, UUID(serviceUUID, 2), []string{"notify"}, nil}, {info, UUID(serviceUUID, 3), []string{"read"}, metadata}} {
		objects[characteristic.path] = map[string]map[string]dbus.Variant{"org.bluez.GattCharacteristic1": {"UUID": dbus.MakeVariant(characteristic.uuid), "Service": dbus.MakeVariant(service), "Flags": dbus.MakeVariant(characteristic.flags), "Value": dbus.MakeVariant(characteristic.metadata)}}
		if err = lifetime.bus.Export(&characteristicObject{metadata: characteristic.metadata, incoming: incoming, done: lifetime.done}, characteristic.path, "org.bluez.GattCharacteristic1"); err != nil {
			return nil, err
		}
	}
	for path, interfaces := range objects {
		if err = lifetime.bus.Export(&readOnlyProperties{interfaces: interfaces}, path, "org.freedesktop.DBus.Properties"); err != nil {
			return nil, err
		}
	}
	if err = lifetime.bus.Export(&objectManager{objects: objects}, root, "org.freedesktop.DBus.ObjectManager"); err != nil {
		return nil, err
	}
	if err = bluezCall(lifetime.bus, adapter, "org.bluez.GattManager1.RegisterApplication", lifetime.done, root, map[string]dbus.Variant{}).Err; err != nil {
		return nil, err
	}
	advertisement := dbus.ObjectPath(string(root) + "/advertisement")
	if err = lifetime.bus.Export(&advertisementObject{}, advertisement, "org.bluez.LEAdvertisement1"); err != nil {
		return nil, err
	}
	if err = lifetime.bus.Export(&readOnlyProperties{interfaces: map[string]map[string]dbus.Variant{"org.bluez.LEAdvertisement1": {"Type": dbus.MakeVariant("peripheral"), "ServiceUUIDs": dbus.MakeVariant([]string{serviceUUID})}}}, advertisement, "org.freedesktop.DBus.Properties"); err != nil {
		return nil, err
	}
	if err = bluezCall(lifetime.bus, adapter, "org.bluez.LEAdvertisingManager1.RegisterAdvertisement", lifetime.done, advertisement, map[string]dbus.Variant{}).Err; err != nil {
		return nil, err
	}
	failed = false
	return &bluezPeripheral{bluezLifetime: lifetime, incoming: incoming, notification: tx, address: strings.TrimPrefix(string(adapter), "/org/bluez/")}, nil
}

type bluezCentral struct {
	*bluezLifetime
	incoming  chan []byte
	writePath dbus.ObjectPath
	metadata  []byte
	address   string
}

func (central *bluezCentral) Packets() <-chan []byte { return central.incoming }
func (central *bluezCentral) Metadata() []byte       { return append([]byte(nil), central.metadata...) }
func (central *bluezCentral) Address() string        { return central.address }
func (central *bluezCentral) Send(payload []byte, done <-chan struct{}) error {
	return bluezCall(central.bus, central.writePath, "org.bluez.GattCharacteristic1.WriteValue", done, payload, map[string]dbus.Variant{"type": dbus.MakeVariant("request")}).Err
}

func (radio BlueZ) Dial(deviceID, serviceUUID string, done <-chan struct{}) (Central, error) {
	lifetime, adapter, err := openBluez(radio.Adapter, done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = lifetime.Close()
		}
	}()
	if len(deviceID) == 12 {
		var parts []string
		for index := 0; index < 12; index += 2 {
			parts = append(parts, deviceID[index:index+2])
		}
		deviceID = strings.Join(parts, ":")
	}
	if _, err = net.ParseMAC(deviceID); err != nil {
		return nil, err
	}
	device := dbus.ObjectPath(string(adapter) + "/dev_" + strings.ReplaceAll(strings.ToUpper(deviceID), ":", "_"))
	if err = bluezCall(lifetime.bus, device, "org.bluez.Device1.Connect", lifetime.done).Err; err != nil {
		return nil, err
	}
	var objects bluezObjects
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var writePath, notifyPath, metadataPath dbus.ObjectPath
discovering:
	for {
		if err = bluezCall(lifetime.bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", lifetime.done).Store(&objects); err != nil {
			return nil, err
		}
		for path, interfaces := range objects {
			if !strings.HasPrefix(string(path), string(device)+"/") {
				continue
			}
			properties := interfaces["org.bluez.GattCharacteristic1"]
			characteristicUUID, _ := properties["UUID"].Value().(string)
			switch characteristicUUID {
			case UUID(serviceUUID, 1):
				writePath = path
			case UUID(serviceUUID, 2):
				notifyPath = path
			case UUID(serviceUUID, 3):
				metadataPath = path
			}
		}
		if writePath != "" && notifyPath != "" && metadataPath != "" {
			break discovering
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return nil, errors.New("netchan: BLE service discovery deadline")
		case <-lifetime.done:
			return nil, net.ErrClosed
		}
	}
	var metadata []byte
	if err = bluezCall(lifetime.bus, metadataPath, "org.bluez.GattCharacteristic1.ReadValue", lifetime.done, map[string]dbus.Variant{}).Store(&metadata); err != nil {
		return nil, err
	}
	if len(metadata) > 113 {
		return nil, errors.New("netchan: oversized BLE metadata")
	}
	signals := make(chan *dbus.Signal, 32)
	lifetime.bus.Signal(signals)
	if err = lifetime.bus.AddMatchSignal(dbus.WithMatchObjectPath(notifyPath), dbus.WithMatchInterface("org.freedesktop.DBus.Properties")); err != nil {
		return nil, err
	}
	if err = bluezCall(lifetime.bus, notifyPath, "org.bluez.GattCharacteristic1.StartNotify", lifetime.done).Err; err != nil {
		return nil, err
	}
	incoming := make(chan []byte, 16)
	go func() {
		defer close(incoming)
		defer lifetime.bus.RemoveSignal(signals)
		for {
			select {
			case signal, open := <-signals:
				if !open {
					return
				}
				if len(signal.Body) < 2 {
					continue
				}
				properties, valid := signal.Body[1].(map[string]dbus.Variant)
				if !valid {
					continue
				}
				payload, valid := properties["Value"].Value().([]byte)
				if !valid || len(payload) > 20 {
					continue
				}
				select {
				case incoming <- append([]byte(nil), payload...):
				case <-lifetime.done:
					return
				}
			case <-lifetime.done:
				return
			}
		}
	}()
	failed = false
	return &bluezCentral{bluezLifetime: lifetime, incoming: incoming, writePath: writePath, metadata: append([]byte(nil), metadata...), address: deviceID}, nil
}

func (radio BlueZ) Scan(serviceUUID string, devices chan<- string, done <-chan struct{}) error {
	lifetime, adapter, err := openBluez(radio.Adapter, done)
	if err != nil {
		return err
	}
	defer lifetime.Close()
	if err = bluezCall(lifetime.bus, adapter, "org.bluez.Adapter1.SetDiscoveryFilter", lifetime.done, map[string]dbus.Variant{"UUIDs": dbus.MakeVariant([]string{serviceUUID}), "Transport": dbus.MakeVariant("le")}).Err; err != nil {
		return err
	}
	if err = bluezCall(lifetime.bus, adapter, "org.bluez.Adapter1.StartDiscovery", lifetime.done).Err; err != nil {
		return err
	}
	defer bluezCall(lifetime.bus, adapter, "org.bluez.Adapter1.StopDiscovery", nil)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	seen := make(map[string]bool)
	for {
		var objects bluezObjects
		if err = bluezCall(lifetime.bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", lifetime.done).Store(&objects); err != nil {
			return err
		}
		for path, interfaces := range objects {
			if !strings.HasPrefix(string(path), string(adapter)+"/") {
				continue
			}
			properties := interfaces["org.bluez.Device1"]
			uuids, _ := properties["UUIDs"].Value().([]string)
			matched := false
			for _, uuid := range uuids {
				if strings.EqualFold(uuid, serviceUUID) {
					matched = true
				}
			}
			address, _ := properties["Address"].Value().(string)
			if !matched || address == "" || seen[address] || len(seen) >= 64 {
				continue
			}
			seen[address] = true
			select {
			case devices <- address:
			case <-lifetime.done:
				return nil
			}
		}
		select {
		case <-ticker.C:
		case <-lifetime.done:
			return nil
		}
	}
}
