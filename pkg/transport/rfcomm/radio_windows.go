//go:build windows && !tinygo

package rfcomm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/matveynator/netchan/v2/pkg/transport"
	"golang.org/x/sys/windows"
)

type Winsock struct{}

func systemRadio() (Radio, error) { return Winsock{}, nil }

// These ABI structures are only used with native Winsock on 64-bit Windows.
// Socket handles belong to one actor; no blocking native call owns the actor.
type bluetoothAddress struct {
	Family  uint16
	_       [6]byte
	Address uint64
	Service windows.GUID
	Port    uint32
	_       [4]byte
}
type socketAPI struct{ library *windows.LazyDLL }

func openSocketAPI() (*socketAPI, error) {
	var startup windows.WSAData
	if err := windows.WSAStartup(0x202, &startup); err != nil {
		return nil, err
	}
	return &socketAPI{library: windows.NewLazySystemDLL("ws2_32.dll")}, nil
}
func (api *socketAPI) call(name string, arguments ...uintptr) (uintptr, error) {
	result, _, _ := api.library.NewProc(name).Call(arguments...)
	if result&0xffffffff == 0xffffffff {
		code, _, _ := api.library.NewProc("WSAGetLastError").Call()
		return result, syscall.Errno(code)
	}
	return result, nil
}
func (api *socketAPI) nonblocking(socket windows.Handle) error {
	enabled := uint32(1)
	_, err := api.call("ioctlsocket", uintptr(socket), 0x8004667e, uintptr(unsafe.Pointer(&enabled))) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
	return err
}
func deviceAddress(device string) (uint64, error) {
	if device == "" || device == "default" {
		return 0, nil
	}
	mac, err := net.ParseMAC(device)
	if err != nil || len(mac) != 6 {
		return 0, errors.New("netchan: invalid Bluetooth MAC address")
	}
	var encoded uint64
	for _, part := range mac {
		encoded = encoded<<8 | uint64(part)
	}
	return encoded, nil
}
func formatAddress(encoded uint64) string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", byte((encoded>>40)&255), byte((encoded>>32)&255), byte((encoded>>24)&255), byte((encoded>>16)&255), byte((encoded>>8)&255), byte(encoded&255))
}

type socketRequest struct {
	payload []byte
	maximum int
	reply   chan socketResult
}
type socketResult struct {
	payload []byte
	count   int
	err     error
}
type socketDeadline struct {
	deadline    time.Time
	read, write bool
	reply       chan error
}
type socketConnection struct {
	api           *socketAPI
	socket        windows.Handle
	local, remote transport.Endpoint
	reads, writes chan socketRequest
	commands      chan any
	done          chan struct{}
}

func newSocketConnection(api *socketAPI, socket windows.Handle, local, remote transport.Endpoint, ownerDone <-chan struct{}) *socketConnection {
	connection := &socketConnection{api: api, socket: socket, local: local, remote: remote, reads: make(chan socketRequest), writes: make(chan socketRequest), commands: make(chan any), done: make(chan struct{})}
	go connection.run(ownerDone)
	return connection
}
func (connection *socketConnection) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	maximum := len(destination)
	if maximum > 16384 {
		maximum = 16384
	}
	reply := make(chan socketResult, 1)
	select {
	case connection.reads <- socketRequest{maximum: maximum, reply: reply}:
	case <-connection.done:
		return 0, net.ErrClosed
	}
	select {
	case result := <-reply:
		return copy(destination, result.payload), result.err
	case <-connection.done:
		return 0, net.ErrClosed
	}
}
func (connection *socketConnection) Write(payload []byte) (int, error) {
	count := 0
	for len(payload) > 0 {
		length := len(payload)
		if length > 16384 {
			length = 16384
		}
		reply := make(chan socketResult, 1)
		select {
		case connection.writes <- socketRequest{payload: append([]byte(nil), payload[:length]...), reply: reply}:
		case <-connection.done:
			return count, net.ErrClosed
		}
		select {
		case result := <-reply:
			count += result.count
			if result.err != nil {
				return count, result.err
			}
			payload = payload[result.count:]
		case <-connection.done:
			return count, net.ErrClosed
		}
	}
	return count, nil
}
func (connection *socketConnection) Close() error {
	select {
	case connection.commands <- struct{}{}:
	case <-connection.done:
	}
	<-connection.done
	return nil
}
func (connection *socketConnection) LocalAddr() net.Addr  { return connection.local }
func (connection *socketConnection) RemoteAddr() net.Addr { return connection.remote }
func (connection *socketConnection) SetDeadline(deadline time.Time) error {
	return connection.deadline(deadline, true, true)
}
func (connection *socketConnection) SetReadDeadline(deadline time.Time) error {
	return connection.deadline(deadline, true, false)
}
func (connection *socketConnection) SetWriteDeadline(deadline time.Time) error {
	return connection.deadline(deadline, false, true)
}
func (connection *socketConnection) deadline(deadline time.Time, read, write bool) error {
	reply := make(chan error, 1)
	select {
	case connection.commands <- socketDeadline{deadline: deadline, read: read, write: write, reply: reply}:
	case <-connection.done:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-connection.done:
		return net.ErrClosed
	}
}
func (connection *socketConnection) run(ownerDone <-chan struct{}) {
	defer close(connection.done)
	defer windows.WSACleanup()
	defer windows.Closesocket(connection.socket)
	var read, write *socketRequest
	var readDeadline, writeDeadline time.Time
	defer func() {
		if read != nil {
			read.reply <- socketResult{err: net.ErrClosed}
		}
		if write != nil {
			write.reply <- socketResult{err: net.ErrClosed}
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var reads, writes chan socketRequest
		if read == nil {
			reads = connection.reads
		}
		if write == nil {
			writes = connection.writes
		}
		var poll <-chan time.Time
		if read != nil || write != nil {
			poll = ticker.C
		}
		select {
		case request := <-reads:
			read = &request
		case request := <-writes:
			write = &request
		case command := <-connection.commands:
			request, ok := command.(socketDeadline)
			if !ok {
				return
			}
			if request.read {
				readDeadline = request.deadline
			}
			if request.write {
				writeDeadline = request.deadline
			}
			request.reply <- nil
		case <-poll:
		case <-ownerDone:
			return
		}
		if read != nil {
			if !readDeadline.IsZero() && !time.Now().Before(readDeadline) {
				read.reply <- socketResult{err: os.ErrDeadlineExceeded}
				read = nil
			} else {
				buffer := make([]byte, read.maximum)
				descriptor := windows.WSABuf{Len: uint32(len(buffer) & 65535), Buf: &buffer[0]}
				var count, flags uint32
				err := windows.WSARecv(connection.socket, &descriptor, 1, &count, &flags, nil, nil)
				if err != windows.WSAEWOULDBLOCK {
					if err == nil && count == 0 {
						err = io.EOF
					}
					read.reply <- socketResult{payload: buffer[:count], err: err}
					read = nil
				}
			}
		}
		if write != nil {
			if !writeDeadline.IsZero() && !time.Now().Before(writeDeadline) {
				write.reply <- socketResult{err: os.ErrDeadlineExceeded}
				write = nil
			} else {
				descriptor := windows.WSABuf{Len: uint32(len(write.payload) & 65535), Buf: &write.payload[0]}
				var count uint32
				err := windows.WSASend(connection.socket, &descriptor, 1, &count, 0, nil, nil)
				if err != windows.WSAEWOULDBLOCK {
					if err == nil && count == 0 {
						err = io.ErrNoProgress
					}
					write.reply <- socketResult{count: int(count), err: err}
					write = nil
				}
			}
		}
	}
}

type windowsListener struct {
	api      *socketAPI
	socket   windows.Handle
	endpoint transport.Endpoint
	accepted chan net.Conn
	stop     chan struct{}
	done     chan struct{}
}

func (listener *windowsListener) Addr() net.Addr { return listener.endpoint }
func (listener *windowsListener) Close() error {
	select {
	case listener.stop <- struct{}{}:
	case <-listener.done:
	}
	<-listener.done
	return nil
}
func (listener *windowsListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.accepted:
		return connection, nil
	case <-listener.done:
		return nil, net.ErrClosed
	}
}
func (Winsock) Listen(endpoint transport.Endpoint, uuid, description string, done <-chan struct{}) (net.Listener, error) {
	api, err := openSocketAPI()
	if err != nil {
		return nil, err
	}
	socket, err := windows.Socket(32, windows.SOCK_STREAM, 3)
	if err != nil {
		_ = windows.WSACleanup()
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = windows.Closesocket(socket)
			_ = windows.WSACleanup()
		}
	}()
	if err = api.nonblocking(socket); err != nil {
		return nil, err
	}
	address, err := deviceAddress(endpoint.Host)
	if err != nil {
		return nil, err
	}
	local := bluetoothAddress{Family: 32, Address: address, Port: ^uint32(0)}
	if _, err = api.call("bind", uintptr(socket), uintptr(unsafe.Pointer(&local)), unsafe.Sizeof(local)); err != nil { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
		return nil, err
	}
	length := int32(unsafe.Sizeof(local))
	if _, err = api.call("getsockname", uintptr(socket), uintptr(unsafe.Pointer(&local)), uintptr(unsafe.Pointer(&length))); err != nil { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
		return nil, err
	}
	if _, err = api.call("listen", uintptr(socket), 8); err != nil {
		return nil, err
	}
	guid, err := windows.GUIDFromString("{" + uuid + "}")
	if err != nil {
		return nil, err
	}
	name, _ := windows.UTF16PtrFromString("NetChan v2")
	comment, _ := windows.UTF16PtrFromString(description)
	addressInfo := windows.CSAddrInfo{LocalAddr: windows.SocketAddress{Sockaddr: (*syscall.RawSockaddrAny)(unsafe.Pointer(&local)), SockaddrLength: int32(unsafe.Sizeof(local))}, SocketType: windows.SOCK_STREAM, Protocol: 3} // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
	query := windows.WSAQUERYSET{Size: uint32(unsafe.Sizeof(windows.WSAQUERYSET{})), ServiceInstanceName: name, ServiceClassId: &guid, Comment: comment, NameSpace: 16, NumberOfCsAddrs: 1, SaBuffer: &addressInfo}
	if _, err = api.call("WSASetServiceW", uintptr(unsafe.Pointer(&query)), 0, 0); err != nil { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
		return nil, err
	}
	endpoint.Host = formatAddress(local.Address)
	listener := &windowsListener{api: api, socket: socket, endpoint: endpoint, accepted: make(chan net.Conn), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(listener.done)
		defer windows.WSACleanup()
		defer windows.Closesocket(socket)
		defer func() {
			_, _ = api.call("WSASetServiceW", uintptr(unsafe.Pointer(&query)), 2, 0) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			runtime.KeepAlive(query)
		}()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-listener.stop:
				return
			case <-ticker.C:
			}
			remote := bluetoothAddress{}
			size := int32(unsafe.Sizeof(remote))
			accepted, acceptErr := api.call("accept", uintptr(socket), uintptr(unsafe.Pointer(&remote)), uintptr(unsafe.Pointer(&size))) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			if acceptErr == windows.WSAEWOULDBLOCK {
				continue
			}
			if acceptErr != nil {
				return
			}
			connectionAPI, startupErr := openSocketAPI()
			if startupErr != nil {
				_ = windows.Closesocket(windows.Handle(accepted))
				continue
			}
			if err := connectionAPI.nonblocking(windows.Handle(accepted)); err != nil {
				_ = windows.Closesocket(windows.Handle(accepted))
				_ = windows.WSACleanup()
				continue
			}
			remoteEndpoint := endpoint
			remoteEndpoint.Host = formatAddress(remote.Address)
			connection := newSocketConnection(connectionAPI, windows.Handle(accepted), endpoint, remoteEndpoint, done)
			select {
			case listener.accepted <- connection:
			case <-done:
				_ = connection.Close()
				return
			case <-listener.stop:
				_ = connection.Close()
				return
			}
		}
	}()
	failed = false
	return listener, nil
}
func (Winsock) Dial(endpoint transport.Endpoint, uuid string, done <-chan struct{}) (net.Conn, error) {
	api, err := openSocketAPI()
	if err != nil {
		return nil, err
	}
	socket, err := windows.Socket(32, windows.SOCK_STREAM, 3)
	if err != nil {
		_ = windows.WSACleanup()
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = windows.Closesocket(socket)
			_ = windows.WSACleanup()
		}
	}()
	if err = api.nonblocking(socket); err != nil {
		return nil, err
	}
	address, err := deviceAddress(endpoint.Host)
	if err != nil {
		return nil, err
	}
	guid, err := windows.GUIDFromString("{" + uuid + "}")
	if err != nil {
		return nil, err
	}
	remote := bluetoothAddress{Family: 32, Address: address, Service: guid}
	_, err = api.call("connect", uintptr(socket), uintptr(unsafe.Pointer(&remote)), unsafe.Sizeof(remote)) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for err == windows.WSAEWOULDBLOCK || err == windows.WSAEINPROGRESS || err == windows.WSAEALREADY {
		select {
		case <-done:
			return nil, net.ErrClosed
		case <-timer.C:
			return nil, os.ErrDeadlineExceeded
		case <-ticker.C:
		}
		poll := struct {
			Socket          windows.Handle
			Events, Revents int16
		}{Socket: socket, Events: 0x10}
		ready, pollErr := api.call("WSAPoll", uintptr(unsafe.Pointer(&poll)), 1, 0) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
		if pollErr != nil {
			return nil, pollErr
		}
		if ready == 0 {
			continue
		}
		var status int32
		length := int32(4)
		if _, socketErr := api.call("getsockopt", uintptr(socket), 0xffff, 0x1007, uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&length))); socketErr != nil { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			return nil, socketErr
		}
		if status != 0 {
			if status < 0 {
				return nil, errors.New("netchan: invalid Winsock connection status")
			}
			return nil, syscall.Errno(status)
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	failed = false
	return newSocketConnection(api, socket, transport.Endpoint{Scheme: "rfcomm", Host: "local", Service: endpoint.Service}, endpoint, done), nil
}

func (Winsock) Discover(uuid string, records chan<- Record, done <-chan struct{}) error {
	api, err := openSocketAPI()
	if err != nil {
		return err
	}
	defer windows.WSACleanup()
	guid, err := windows.GUIDFromString("{" + uuid + "}")
	if err != nil {
		return err
	}
	query := windows.WSAQUERYSET{Size: uint32(unsafe.Sizeof(windows.WSAQUERYSET{})), NameSpace: 16}
	return api.lookup(&query, 0x112, done, func(device *windows.WSAQUERYSET, buffer []byte) error {
		if device.NumberOfCsAddrs == 0 || !inside(buffer, unsafe.Pointer(device.SaBuffer), unsafe.Sizeof(windows.CSAddrInfo{})) { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			return nil
		}
		remote := device.SaBuffer.RemoteAddr
		if remote.SockaddrLength < int32(unsafe.Sizeof(bluetoothAddress{})) || !inside(buffer, unsafe.Pointer(remote.Sockaddr), unsafe.Sizeof(bluetoothAddress{})) { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			return nil
		}
		address := (*bluetoothAddress)(unsafe.Pointer(remote.Sockaddr)) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
		if address.Family != 32 {
			return nil
		}
		identifier := formatAddress(address.Address)
		scope, _ := windows.UTF16PtrFromString("(" + identifier + ")")
		service := windows.WSAQUERYSET{Size: query.Size, NameSpace: 16, ServiceClassId: &guid, Context: scope}
		return api.lookup(&service, 0x190, done, func(result *windows.WSAQUERYSET, response []byte) error {
			if !inside(response, unsafe.Pointer(result.Comment), 2) { // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
				return nil
			}
			pointer := uintptr(unsafe.Pointer(result.Comment))                                  // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			end := uintptr(unsafe.Pointer(unsafe.SliceData(response))) + uintptr(len(response)) // #nosec G103 -- Winsock ABI uses Go-owned arguments; returned pointers are checked by inside before dereference.
			maximum := int((end - pointer) / 2)
			if maximum > 320 {
				maximum = 320
			}
			encoded := unsafe.Slice(result.Comment, maximum) // #nosec G103 -- inside validates the comment address; maximum is bounded by the remaining response buffer and 320 UTF-16 units.
			length := 0
			for length < len(encoded) && encoded[length] != 0 {
				length++
			}
			if length == len(encoded) {
				return nil
			}
			select {
			case records <- Record{Device: identifier, Metadata: windows.UTF16ToString(encoded[:length])}:
				return nil
			case <-done:
				return net.ErrClosed
			}
		})
	})
}
func inside(buffer []byte, pointer unsafe.Pointer, length uintptr) bool {
	start := uintptr(unsafe.Pointer(unsafe.SliceData(buffer))) // #nosec G103 -- Address arithmetic only bounds-checks native result pointers against the Go-owned response buffer.
	address := uintptr(pointer)
	return address >= start && length <= uintptr(len(buffer)) && address-start <= uintptr(len(buffer))-length
}
func (api *socketAPI) lookup(query *windows.WSAQUERYSET, flags uint32, done <-chan struct{}, receive func(*windows.WSAQUERYSET, []byte) error) error {
	var handle windows.Handle
	if err := windows.WSALookupServiceBegin(query, flags, &handle); err != nil {
		return err
	}
	finished, watcherDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-done:
		case <-finished:
		}
		_ = windows.WSALookupServiceEnd(handle)
	}()
	defer func() { close(finished); <-watcherDone }()
	for index := 0; index < 64; index++ {
		buffer := make([]byte, 65536)
		size := int32(65536)
		result := (*windows.WSAQUERYSET)(unsafe.Pointer(&buffer[0])) // #nosec G103 -- The Go-owned 64 KiB response buffer is aligned and larger than WSAQUERYSET; Winsock receives its exact size.
		err := windows.WSALookupServiceNext(handle, flags, &size, result)
		if err == windows.WSA_E_NO_MORE || err == windows.WSAENOMORE {
			return nil
		}
		if err != nil {
			return err
		}
		if err = receive(result, buffer); err != nil {
			return err
		}
	}
	return nil
}
