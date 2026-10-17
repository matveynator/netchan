//go:build windows && !tinygo

package ble

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/saltosystems/winrt-go"
	bt "github.com/saltosystems/winrt-go/windows/devices/bluetooth"
	gatt "github.com/saltosystems/winrt-go/windows/devices/bluetooth/genericattributeprofile"
	"github.com/saltosystems/winrt-go/windows/foundation"
	"github.com/saltosystems/winrt-go/windows/foundation/collections"
	"github.com/saltosystems/winrt-go/windows/storage/streams"
	"golang.org/x/sys/windows"
	"tinygo.org/x/bluetooth"
)

type windowsRadio struct{}

func systemRadio(done <-chan struct{}) (Radio, error) { return windowsRadio{}, nil }
func (windowsRadio) Dial(device, uuid string, done <-chan struct{}) (Central, error) {
	return NewSDKRadio(&bluetooth.Adapter{}).Dial(device, uuid, done)
}
func (windowsRadio) Scan(uuid string, devices chan<- string, done <-chan struct{}) error {
	return NewSDKRadio(&bluetooth.Adapter{}).Scan(uuid, devices, done)
}

func enterSDKThread() (func(), error) {
	runtime.LockOSThread()
	library := windows.NewLazySystemDLL("combase.dll")
	result, _, _ := library.NewProc("RoInitialize").Call(1)
	if result != 0 && result != 1 {
		runtime.UnlockOSThread()
		return nil, ole.NewError(result)
	}
	return func() { _, _, _ = library.NewProc("RoUninitialize").Call(); runtime.UnlockOSThread() }, nil
}
func enableSDKAdapter(adapter *bluetooth.Adapter) error { return nil }

// OS-owned operation state avoids mutable Go callback registries. Cancellation
// reaches the native operation as well as the caller waiting for its result.
func awaitWindows(operation *foundation.IAsyncOperation, done <-chan struct{}) error {
	unknown, err := operation.QueryInterface(ole.NewGUID(foundation.GUIDIAsyncInfo))
	if err != nil {
		return err
	}
	defer unknown.Release()
	info := (*foundation.IAsyncInfo)(unsafe.Pointer(unknown)) // #nosec G103 -- QueryInterface supplies a retained COM interface of the exact IID; the WinRT ABI vtable and output arguments have matching layouts.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		status, statusErr := info.GetStatus()
		if statusErr != nil {
			return statusErr
		}
		if status == foundation.AsyncStatusCompleted {
			return nil
		}
		if status != foundation.AsyncStatusStarted {
			code, codeErr := info.GetErrorCode()
			if codeErr != nil {
				return codeErr
			}
			return fmt.Errorf("netchan: WinRT status %d, HRESULT %x", status, code.Value)
		}
		select {
		case <-ticker.C:
		case <-done:
			_ = info.Cancel()
			return net.ErrClosed
		case <-timer.C:
			_ = info.Cancel()
			return transport.ErrUnavailable
		}
	}
}
func windowsBuffer(payload []byte) (*streams.IBuffer, error) {
	if len(payload) > 65535 {
		return nil, errors.New("netchan: oversized native GATT buffer")
	}
	writer, err := streams.NewDataWriter()
	if err != nil {
		return nil, err
	}
	defer writer.Release()
	if err = writer.WriteBytes(uint32(len(payload)&65535), payload); err != nil {
		return nil, err
	}
	return writer.DetachBuffer()
}
func windowsUUID(text string) syscall.GUID {
	guid := ole.NewGUID(text)
	return syscall.GUID{Data1: guid.Data1, Data2: guid.Data2, Data3: guid.Data3, Data4: guid.Data4}
}

// The generated SDK omits IGattSession.DeviceId. Its first ABI property supplies
// the native identity required to isolate concurrent centrals.
func windowsPeer(session *gatt.GattSession) (string, error) {
	unknown, err := session.QueryInterface(ole.NewGUID(gatt.GUIDiGattSession))
	if err != nil {
		return "", err
	}
	defer unknown.Release()
	type sessionTable struct {
		ole.IInspectableVtbl
		GetDeviceID uintptr
	}
	var device *bt.BluetoothDeviceId
	table := (*sessionTable)(unsafe.Pointer(unknown.RawVTable))                                                             // #nosec G103 -- QueryInterface supplies a retained COM interface of the exact IID; the WinRT ABI vtable and output arguments have matching layouts.
	result, _, _ := syscall.SyscallN(table.GetDeviceID, uintptr(unsafe.Pointer(unknown)), uintptr(unsafe.Pointer(&device))) // #nosec G103 -- QueryInterface supplies a retained COM interface of the exact IID; the WinRT ABI vtable and output arguments have matching layouts.
	if result != 0 {
		return "", ole.NewError(result)
	}
	if device == nil {
		return "", errors.New("netchan: missing GATT peer")
	}
	defer device.Release()
	return device.GetId()
}

type windowsPeripheral struct {
	incoming   chan Received
	commands   chan sdkSend
	stop, done chan struct{}
}

func (peripheral *windowsPeripheral) Address() string          { return "default" }
func (peripheral *windowsPeripheral) Packets() <-chan Received { return peripheral.incoming }
func (peripheral *windowsPeripheral) Close() error {
	select {
	case peripheral.stop <- struct{}{}:
	case <-peripheral.done:
	}
	<-peripheral.done
	return nil
}
func (peripheral *windowsPeripheral) Send(peer string, payload []byte, done <-chan struct{}) error {
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

type windowsListenResult struct {
	peripheral Peripheral
	err        error
}

func (windowsRadio) Listen(uuid string, metadata []byte, done <-chan struct{}) (Peripheral, error) {
	ready := make(chan windowsListenResult, 1)
	go func() {
		if _, err := listenWindows(uuid, metadata, done, ready); err != nil {
			ready <- windowsListenResult{err: err}
		}
	}()
	result := <-ready
	return result.peripheral, result.err
}

func listenWindows(uuid string, metadata []byte, done <-chan struct{}, ready chan<- windowsListenResult) (Peripheral, error) {
	leave, err := enterSDKThread()
	if err != nil {
		return nil, err
	}
	defer leave()
	operation, err := gatt.GattServiceProviderCreateAsync(windowsUUID(uuid))
	if err != nil {
		return nil, err
	}
	defer operation.Release()
	if err = awaitWindows(operation, done); err != nil {
		return nil, err
	}
	resultPointer, err := operation.GetResults()
	if err != nil {
		return nil, err
	}
	result := (*gatt.GattServiceProviderResult)(resultPointer)
	defer result.Release()
	provider, err := result.GetServiceProvider()
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, transport.ErrUnavailable
	}
	failed := true
	var release []func()
	defer func() {
		if failed {
			_ = provider.StopAdvertising()
			for index := len(release) - 1; index >= 0; index-- {
				release[index]()
			}
			provider.Release()
		}
	}()
	service, err := provider.GetService()
	if err != nil {
		return nil, err
	}
	defer service.Release()
	create := func(characteristicUUID string, flags gatt.GattCharacteristicProperties, payload []byte) (*gatt.GattLocalCharacteristic, error) {
		parameters, createErr := gatt.NewGattLocalCharacteristicParameters()
		if createErr != nil {
			return nil, createErr
		}
		defer parameters.Release()
		if createErr = parameters.SetCharacteristicProperties(flags); createErr != nil {
			return nil, createErr
		}
		if payload != nil {
			buffer, bufferErr := windowsBuffer(payload)
			if bufferErr != nil {
				return nil, bufferErr
			}
			defer buffer.Release()
			if createErr = parameters.SetStaticValue(buffer); createErr != nil {
				return nil, createErr
			}
		}
		request, createErr := service.CreateCharacteristicAsync(windowsUUID(characteristicUUID), parameters)
		if createErr != nil {
			return nil, createErr
		}
		defer request.Release()
		if createErr = awaitWindows(request, done); createErr != nil {
			return nil, createErr
		}
		pointer, createErr := request.GetResults()
		if createErr != nil {
			return nil, createErr
		}
		created := (*gatt.GattLocalCharacteristicResult)(pointer)
		defer created.Release()
		characteristic, createErr := created.GetCharacteristic()
		if createErr != nil {
			return nil, createErr
		}
		if characteristic == nil {
			return nil, transport.ErrUnavailable
		}
		release = append(release, func() { characteristic.Release() })
		return characteristic, nil
	}
	rx, err := create(UUID(uuid, 1), gatt.GattCharacteristicPropertiesWrite, nil)
	if err != nil {
		return nil, err
	}
	tx, err := create(UUID(uuid, 2), gatt.GattCharacteristicPropertiesNotify, nil)
	if err != nil {
		return nil, err
	}
	if _, err = create(UUID(uuid, 3), gatt.GattCharacteristicPropertiesRead, metadata); err != nil {
		return nil, err
	}
	peripheral := &windowsPeripheral{incoming: make(chan Received, 32), commands: make(chan sdkSend), stop: make(chan struct{}, 1), done: make(chan struct{})}
	canceled := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		defer close(canceled)
		select {
		case <-done:
		case <-peripheral.stop:
		case <-peripheral.done:
		}
	}()
	defer func() {
		if failed {
			select {
			case peripheral.stop <- struct{}{}:
			default:
			}
			<-watcherDone
		}
	}()
	handlerID := winrt.ParameterizedInstanceGUID(foundation.GUIDTypedEventHandler, gatt.SignatureGattLocalCharacteristic, gatt.SignatureGattWriteRequestedEventArgs)
	handler := foundation.NewTypedEventHandler(ole.NewGUID(handlerID), func(_ *foundation.TypedEventHandler, sender, arguments unsafe.Pointer) {
		args := (*gatt.GattWriteRequestedEventArgs)(arguments)
		deferral, nativeErr := args.GetDeferral()
		if nativeErr != nil {
			return
		}
		defer deferral.Release()
		defer deferral.Complete()
		session, nativeErr := args.GetSession()
		if nativeErr != nil {
			return
		}
		defer session.Release()
		peer, nativeErr := windowsPeer(session)
		if nativeErr != nil {
			return
		}
		requestOp, nativeErr := args.GetRequestAsync()
		if nativeErr != nil {
			return
		}
		defer requestOp.Release()
		if nativeErr = awaitWindows(requestOp, canceled); nativeErr != nil {
			return
		}
		pointer, nativeErr := requestOp.GetResults()
		if nativeErr != nil || pointer == nil {
			return
		}
		request := (*gatt.GattWriteRequest)(pointer)
		defer request.Release()
		offset, nativeErr := request.GetOffset()
		if nativeErr != nil || offset != 0 {
			_ = request.RespondWithProtocolError(7)
			return
		}
		buffer, nativeErr := request.GetValue()
		if nativeErr != nil {
			return
		}
		defer buffer.Release()
		length, nativeErr := buffer.GetLength()
		if nativeErr != nil || length < envelopeSize || length > 20 {
			_ = request.RespondWithProtocolError(13)
			return
		}
		reader, nativeErr := streams.DataReaderFromBuffer(buffer)
		if nativeErr != nil {
			return
		}
		defer reader.Release()
		payload, nativeErr := reader.ReadBytes(length)
		if nativeErr != nil {
			return
		}
		select {
		case peripheral.incoming <- Received{Peer: peer, Payload: payload}:
			_ = request.Respond()
		case <-canceled:
			_ = request.RespondWithProtocolError(14)
		default:
			_ = request.RespondWithProtocolError(17)
		}
	})
	release = append(release, func() { handler.Release() })
	token, err := rx.AddWriteRequested(handler)
	if err != nil {
		return nil, err
	}
	release = append(release, func() { _ = rx.RemoveWriteRequested(token) })
	parameters, err := gatt.NewGattServiceProviderAdvertisingParameters()
	if err != nil {
		return nil, err
	}
	defer parameters.Release()
	if err = parameters.SetIsConnectable(true); err != nil {
		return nil, err
	}
	if err = parameters.SetIsDiscoverable(true); err != nil {
		return nil, err
	}
	if err = provider.StartAdvertisingWithParameters(parameters); err != nil {
		return nil, err
	}
	failed = false
	ready <- windowsListenResult{peripheral: peripheral}
	func() {
		defer close(peripheral.done)
		defer provider.Release()
		defer func() {
			_ = provider.StopAdvertising()
			for index := len(release) - 1; index >= 0; index-- {
				release[index]()
			}
		}()
		notify := func(payload []byte) error {
			buffer, notifyErr := windowsBuffer(payload)
			if notifyErr != nil {
				return notifyErr
			}
			defer buffer.Release()
			request, notifyErr := tx.NotifyValueAsync(buffer)
			if notifyErr != nil {
				return notifyErr
			}
			defer request.Release()
			if notifyErr = awaitWindows(request, canceled); notifyErr != nil {
				return notifyErr
			}
			pointer, notifyErr := request.GetResults()
			if pointer != nil {
				(*collections.IVectorView)(pointer).Release()
			}
			return notifyErr
		}
		for {
			select {
			case request := <-peripheral.commands:
				request.reply <- notify(request.payload)
			case <-canceled:
				return
			}
		}
	}()
	failed = false
	return peripheral, nil
}
