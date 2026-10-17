//go:build darwin && (amd64 || arm64) && !tinygo

package ble

import (
	"reflect"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/matveynator/netchan/v2/pkg/transport/apple"
)

// Native callback trampolines are process resources that purego cannot free.
// This immutable ABI is initialized once; each delegate carries only a Go-only
// pinned, opaque pointer to its actor's channels, never a shared registry.
type appleNativeABI struct {
	class        objc.Class
	createQueue  func(string, uintptr) uintptr
	releaseQueue func(uintptr)
	syncQueue    func(uintptr, unsafe.Pointer, uintptr)
	invoke       uintptr
	err          error
}

var appleABI = defineAppleABI()

type appleEventSink struct {
	events   chan<- appleEvent
	overflow chan<- struct{}
	done     <-chan struct{}
}

func defineAppleABI() appleNativeABI {
	var definition appleNativeABI
	for _, framework := range []string{"Foundation", "CoreBluetooth"} {
		if _, definition.err = apple.Load(framework); definition.err != nil {
			return definition
		}
	}
	lib, err := purego.Dlopen("/usr/lib/system/libdispatch.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		definition.err = err
		return definition
	}
	purego.RegisterLibFunc(&definition.createQueue, lib, "dispatch_queue_create")
	purego.RegisterLibFunc(&definition.releaseQueue, lib, "dispatch_release")
	purego.RegisterLibFunc(&definition.syncQueue, lib, "dispatch_sync_f")
	definition.invoke = purego.NewCallback(func(pointer unsafe.Pointer) { (*appleQueueJob)(pointer).run() }) // #nosec G103 -- perform pins this opaque job until the synchronous native callback returns.
	methods := []objc.MethodDef{
		{Cmd: apple.Select("centralManagerDidUpdateState:"), Fn: func(self objc.ID, _ objc.SEL, manager objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "state", first: manager})
		}},
		{Cmd: apple.Select("peripheralManagerDidUpdateState:"), Fn: func(self objc.ID, _ objc.SEL, manager objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "state", first: manager})
		}},
		{Cmd: apple.Select("centralManager:didDiscoverPeripheral:advertisementData:RSSI:"), Fn: func(self objc.ID, _ objc.SEL, manager, peripheral, advertisement, rssi objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "discovered", first: peripheral, second: advertisement})
		}},
		{Cmd: apple.Select("centralManager:didConnectPeripheral:"), Fn: func(self objc.ID, _ objc.SEL, manager, peripheral objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "connected", first: peripheral})
		}},
		{Cmd: apple.Select("centralManager:didFailToConnectPeripheral:error:"), Fn: func(self objc.ID, _ objc.SEL, manager, peripheral, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "failed", first: peripheral, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("centralManager:didDisconnectPeripheral:error:"), Fn: func(self objc.ID, _ objc.SEL, manager, peripheral, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "disconnected", first: peripheral, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheral:didDiscoverServices:"), Fn: func(self objc.ID, _ objc.SEL, peripheral, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "services", first: peripheral, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheral:didDiscoverCharacteristicsForService:error:"), Fn: func(self objc.ID, _ objc.SEL, peripheral, service, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "characteristics", first: peripheral, second: service, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheral:didUpdateValueForCharacteristic:error:"), Fn: func(self objc.ID, _ objc.SEL, peripheral, characteristic, nativeError objc.ID) {
			payload, err := apple.Bytes(characteristic.Send(apple.Select("value")), 512)
			if err == nil {
				err = apple.Error(nativeError)
			}
			emitAppleEvent(appleEvent{delegate: self, kind: "value", first: characteristic, payload: payload, err: err})
		}},
		{Cmd: apple.Select("peripheral:didWriteValueForCharacteristic:error:"), Fn: func(self objc.ID, _ objc.SEL, peripheral, characteristic, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "written", first: characteristic, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheral:didUpdateNotificationStateForCharacteristic:error:"), Fn: func(self objc.ID, _ objc.SEL, peripheral, characteristic, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "subscribed", first: characteristic, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheralManager:didAddService:error:"), Fn: func(self objc.ID, _ objc.SEL, manager, service, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "added", first: service, err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheralManagerDidStartAdvertising:error:"), Fn: func(self objc.ID, _ objc.SEL, manager, nativeError objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "advertising", err: apple.Error(nativeError)})
		}},
		{Cmd: apple.Select("peripheralManager:didReceiveWriteRequests:"), Fn: func(self objc.ID, _ objc.SEL, manager, requests objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "writes", first: requests})
		}},
		{Cmd: apple.Select("peripheralManager:didReceiveReadRequest:"), Fn: func(self objc.ID, _ objc.SEL, manager, request objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "read", first: request})
		}},
		{Cmd: apple.Select("peripheralManager:central:didSubscribeToCharacteristic:"), Fn: func(self objc.ID, _ objc.SEL, manager, central, characteristic objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "central", first: central})
		}},
		{Cmd: apple.Select("peripheralManagerIsReadyToUpdateSubscribers:"), Fn: func(self objc.ID, _ objc.SEL, manager objc.ID) {
			emitAppleEvent(appleEvent{delegate: self, kind: "writable"})
		}},
	}
	definition.class, definition.err = objc.RegisterClass("NetChanBLEDelegateV2", objc.GetClass("NSObject"), nil,
		[]objc.FieldDef{{Name: "netchanSink", Type: reflect.TypeFor[unsafe.Pointer](), Attribute: objc.ReadWrite}}, methods)
	return definition
}

func emitAppleEvent(event appleEvent) {
	pointer := objc.Send[unsafe.Pointer](event.delegate, apple.Select("netchanSink"))
	if pointer == nil {
		return
	}
	sink := (*appleEventSink)(pointer) // #nosec G103 -- The session pins this immutable channel sink until its serial native queue has detached every delegate.
	// A queued event must keep its delegate address alive until delivery, so
	// native address reuse cannot route it to a later session.
	event.delegate.Send(apple.Select("retain"))
	if event.first != 0 {
		event.first.Send(apple.Select("retain"))
	}
	if event.second != 0 {
		event.second.Send(apple.Select("retain"))
	}
	select {
	case sink.events <- event:
	case <-sink.done:
		event.release()
	default:
		event.release()
		select {
		case sink.overflow <- struct{}{}:
		default:
		}
	}
}

type appleQueueJob struct{ run func() }

// The actor joins the serial native queue before unpinning its callback target.
// Native code only relays these opaque pointers; all fields are read by Go.
func (definition appleNativeABI) perform(queue uintptr, job func()) {
	target := &appleQueueJob{run: job}
	var pinned runtime.Pinner
	pinned.Pin(target)
	defer pinned.Unpin()
	definition.syncQueue(queue, unsafe.Pointer(target), definition.invoke) // #nosec G103 -- dispatch_sync_f returns after the pinned callback has completed.
	runtime.KeepAlive(target)
}
