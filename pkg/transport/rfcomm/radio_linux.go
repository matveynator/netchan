//go:build linux && !baremetal && !tinygo

package rfcomm

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/matveynator/netchan/v2/pkg/transport"
	"golang.org/x/sys/unix"
)

// BlueZ owns profile sockets and SDP publication; each operation has a private
// bus connection so registration and cancellation cannot affect another owner.
type BlueZ struct{}

func systemRadio() (Radio, error) { return BlueZ{}, nil }

type profileConnection struct {
	device dbus.ObjectPath
	file   *os.File
	reply  chan error
}
type disconnectProfile struct {
	device dbus.ObjectPath
	reply  chan struct{}
}
type profile struct {
	commands chan any
	done     <-chan struct{}
}

func (profile *profile) Release() *dbus.Error {
	select {
	case profile.commands <- struct{}{}:
	case <-profile.done:
	}
	return nil
}
func (profile *profile) NewConnection(device dbus.ObjectPath, descriptor dbus.UnixFD, properties map[string]dbus.Variant) *dbus.Error {
	fd := int(descriptor)
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return dbus.MakeFailedError(err)
	}
	file := os.NewFile(uintptr(fd), "netchan-rfcomm")
	if file == nil {
		_ = unix.Close(fd)
		return dbus.MakeFailedError(net.ErrClosed)
	}
	reply := make(chan error, 1)
	select {
	case profile.commands <- profileConnection{device: device, file: file, reply: reply}:
	case <-profile.done:
		_ = file.Close()
		return dbus.MakeFailedError(net.ErrClosed)
	}
	select {
	case err := <-reply:
		if err != nil {
			return dbus.MakeFailedError(err)
		}
		return nil
	case <-profile.done:
		return dbus.MakeFailedError(net.ErrClosed)
	}
}
func (profile *profile) RequestDisconnection(device dbus.ObjectPath) *dbus.Error {
	reply := make(chan struct{}, 1)
	select {
	case profile.commands <- disconnectProfile{device: device, reply: reply}:
	case <-profile.done:
		return nil
	}
	select {
	case <-reply:
	case <-profile.done:
	}
	return nil
}

type linuxProfile struct {
	bus      *dbus.Conn
	commands chan any
	closed   chan *os.File
	accepted chan net.Conn
	stop     chan struct{}
	done     chan struct{}
	endpoint transport.Endpoint
}
type fileConnection struct {
	*os.File
	local, remote net.Addr
	closed        chan<- *os.File
	done          <-chan struct{}
}

func (connection *fileConnection) Close() error {
	err := connection.File.Close()
	select {
	case connection.closed <- connection.File:
	case <-connection.done:
	}
	return err
}

func (connection *fileConnection) LocalAddr() net.Addr  { return connection.local }
func (connection *fileConnection) RemoteAddr() net.Addr { return connection.remote }

func call(bus *dbus.Conn, path dbus.ObjectPath, method string, done <-chan struct{}, arguments ...any) *dbus.Call {
	boundary, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	finished := make(chan struct{})
	go func() {
		select {
		case <-done:
			cancel()
		case <-finished:
		}
	}()
	result := bus.Object("org.bluez", path).CallWithContext(boundary, method, 0, arguments...)
	close(finished)
	cancel()
	return result
}

func openProfile(endpoint transport.Endpoint, uuid, description, role string, done <-chan struct{}) (*linuxProfile, error) {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	listener := &linuxProfile{bus: bus, commands: make(chan any), closed: make(chan *os.File), accepted: make(chan net.Conn), stop: make(chan struct{}), done: make(chan struct{}), endpoint: endpoint}
	go listener.run(done)
	path := dbus.ObjectPath("/netchan/profile")
	if err = bus.Export(&profile{commands: listener.commands, done: listener.done}, path, "org.bluez.Profile1"); err != nil {
		_ = listener.Close()
		return nil, err
	}
	options := map[string]dbus.Variant{"Name": dbus.MakeVariant("NetChan v2"), "Role": dbus.MakeVariant(role), "RequireAuthentication": dbus.MakeVariant(false), "RequireAuthorization": dbus.MakeVariant(false)}
	if role == "server" {
		var escaped bytes.Buffer
		if err = xml.EscapeText(&escaped, []byte(description)); err != nil {
			_ = listener.Close()
			return nil, err
		}
		// BlueZ allocates the RFCOMM channel and substitutes it into its record.
		// ServiceDescription carries discovery hints, never authentication.
		options["ServiceRecord"] = dbus.MakeVariant(fmt.Sprintf(`<record><attribute id="0x0001"><sequence><uuid value="%s"/></sequence></attribute><attribute id="0x0004"><sequence><sequence><uuid value="0x0100"/></sequence><sequence><uuid value="0x0003"/><uint8 value="0"/></sequence></sequence></attribute><attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute><attribute id="0x0100"><text value="NetChan v2"/></attribute><attribute id="0x0101"><text value="%s"/></attribute></record>`, uuid, escaped.String()))
	}
	if err = call(bus, "/org/bluez", "org.bluez.ProfileManager1.RegisterProfile", listener.done, path, uuid, options).Err; err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
func (listener *linuxProfile) run(ownerDone <-chan struct{}) {
	connections := make(map[*os.File]dbus.ObjectPath)
	defer func() {
		close(listener.done)
		for file := range connections {
			_ = file.Close()
		}
		_ = listener.bus.Close()
	}()
	var pending []net.Conn
	for {
		var accepted chan net.Conn
		var next net.Conn
		if len(pending) > 0 {
			accepted, next = listener.accepted, pending[0]
		}
		select {
		case file := <-listener.closed:
			delete(connections, file)
		case accepted <- next:
			pending = pending[1:]
		case command := <-listener.commands:
			switch request := command.(type) {
			case struct{}:
				return
			case profileConnection:
				if len(pending) >= 8 || len(connections) >= 256 {
					_ = request.file.Close()
					request.reply <- transport.ErrUnavailable
					continue
				}
				connections[request.file] = request.device
				remote := listener.endpoint
				remote.Host = strings.ReplaceAll(strings.TrimPrefix(string(request.device[strings.LastIndex(string(request.device), "/")+1:]), "dev_"), "_", ":")
				pending = append(pending, &fileConnection{File: request.file, local: listener.endpoint, remote: remote, closed: listener.closed, done: listener.done})
				request.reply <- nil
			case disconnectProfile:
				for file, device := range connections {
					if device == request.device {
						_ = file.Close()
						delete(connections, file)
					}
				}
				request.reply <- struct{}{}
			}
		case <-listener.stop:
			return
		case <-ownerDone:
			return
		}
	}
}
func (listener *linuxProfile) Addr() net.Addr { return listener.endpoint }
func (listener *linuxProfile) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.accepted:
		return connection, nil
	case <-listener.done:
		return nil, net.ErrClosed
	}
}
func (listener *linuxProfile) Close() error {
	select {
	case listener.stop <- struct{}{}:
	case <-listener.done:
	}
	<-listener.done
	return nil
}
func (BlueZ) Listen(endpoint transport.Endpoint, uuid, description string, done <-chan struct{}) (net.Listener, error) {
	return openProfile(endpoint, uuid, description, "server", done)
}

type linuxClient struct {
	net.Conn
	profile *linuxProfile
}

func (connection *linuxClient) Close() error {
	err := connection.Conn.Close()
	_ = connection.profile.Close()
	return err
}
func (connection *linuxClient) Underlying() net.Conn { return connection.Conn }
func (BlueZ) Dial(endpoint transport.Endpoint, uuid string, done <-chan struct{}) (net.Conn, error) {
	profile, err := openProfile(endpoint, uuid, "", "client", done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = profile.Close()
		}
	}()
	mac, err := net.ParseMAC(endpoint.Host)
	if err != nil || len(mac) != 6 {
		return nil, errors.New("netchan: RFCOMM requires a Bluetooth MAC address")
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err = call(profile.bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", done).Store(&objects); err != nil {
		return nil, err
	}
	var device dbus.ObjectPath
	for path, interfaces := range objects {
		address, _ := interfaces["org.bluez.Device1"]["Address"].Value().(string)
		if strings.EqualFold(address, endpoint.Host) {
			device = path
			break
		}
	}
	if device == "" {
		return nil, fmt.Errorf("%w: discover or pair Bluetooth device first", transport.ErrPeerNotFound)
	}
	if err = call(profile.bus, device, "org.bluez.Device1.ConnectProfile", done, uuid).Err; err != nil {
		return nil, err
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case connection := <-profile.accepted:
		failed = false
		return &linuxClient{Conn: connection, profile: profile}, nil
	case <-profile.done:
		return nil, net.ErrClosed
	case <-timer.C:
		return nil, os.ErrDeadlineExceeded
	}
}

func (BlueZ) Discover(uuid string, records chan<- Record, done <-chan struct{}) error {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer bus.Close()
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err = call(bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", done).Store(&objects); err != nil {
		return err
	}
	var adapters []dbus.ObjectPath
	for path, interfaces := range objects {
		if _, ok := interfaces["org.bluez.Adapter1"]; ok {
			adapters = append(adapters, path)
			_ = call(bus, path, "org.bluez.Adapter1.StartDiscovery", done).Err
		}
	}
	defer func() {
		for _, adapter := range adapters {
			_ = call(bus, adapter, "org.bluez.Adapter1.StopDiscovery", nil).Err
		}
	}()
	seen := make(map[string]bool)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, interfaces := range objects {
			device := interfaces["org.bluez.Device1"]
			address, _ := device["Address"].Value().(string)
			if address == "" || seen[address] || len(seen) >= 64 {
				continue
			}
			uuids, _ := device["UUIDs"].Value().([]string)
			matched := false
			for _, advertised := range uuids {
				if strings.EqualFold(advertised, uuid) {
					matched = true
				}
			}
			if !matched {
				continue
			}
			seen[address] = true
			description, queryErr := querySDP(address, uuid, done)
			if queryErr != nil {
				continue
			}
			select {
			case records <- Record{Device: address, Metadata: description}:
			case <-done:
				return nil
			}
		}
		select {
		case <-done:
			return nil
		case <-ticker.C:
		}
		if err = call(bus, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects", done).Store(&objects); err != nil {
			return err
		}
	}
}
