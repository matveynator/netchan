//go:build darwin && (amd64 || arm64) && !tinygo

package ble

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/matveynator/netchan/v2/pkg/transport"
	"github.com/matveynator/netchan/v2/pkg/transport/apple"
)

type appleEvent struct {
	delegate      objc.ID
	kind          string
	first, second objc.ID
	payload       []byte
	err           error
}
type appleOpen struct {
	peripheral bool
	done       <-chan struct{}
	reply      chan appleOpenResult
}
type appleOpenResult struct {
	session *appleSession
	err     error
}
type appleRemove struct {
	session *appleSession
	reply   chan struct{}
}
type appleSession struct {
	manager, delegate objc.ID
	events            chan appleEvent
	radio             *appleRadio
	done              <-chan struct{}
	stop              chan struct{}
	target            *appleEventSink
	pinned            runtime.Pinner
}
type appleRadio struct {
	commands  chan any
	ownerDone <-chan struct{}
	finished  chan struct{}
}

func systemRadio(done <-chan struct{}) (Radio, error) {
	radio := &appleRadio{commands: make(chan any), ownerDone: done, finished: make(chan struct{})}
	go radio.run()
	return radio, nil
}

func (radio *appleRadio) open(peripheral bool, done <-chan struct{}) (*appleSession, error) {
	reply := make(chan appleOpenResult, 1)
	select {
	case radio.commands <- appleOpen{peripheral: peripheral, done: done, reply: reply}:
	case <-done:
		return nil, net.ErrClosed
	case <-radio.ownerDone:
		return nil, net.ErrClosed
	}
	result := <-reply
	return result.session, result.err
}
func (session *appleSession) close() {
	select {
	case session.stop <- struct{}{}:
	default:
	}
	reply := make(chan struct{}, 1)
	select {
	case session.radio.commands <- appleRemove{session: session, reply: reply}:
	case <-session.radio.finished:
		return
	}
	select {
	case <-reply:
	case <-session.radio.finished:
	}
}

func (radio *appleRadio) run() {
	defer close(radio.finished)
	ownerDone := radio.ownerDone
	stopping := false
	events := make(chan appleEvent, 64)
	overflow := make(chan struct{}, 1)
	sessions := make(map[objc.ID]*appleSession)
	var nativeQueue uintptr
	cleanup := func(session *appleSession) {
		if sessions[session.delegate] != session {
			return
		}
		appleABI.perform(nativeQueue, func() {
			session.manager.Send(apple.Select("setDelegate:"), objc.ID(0))
			session.delegate.Send(apple.Select("setNetchanSink:"), unsafe.Pointer(nil)) // #nosec G103 -- Clear the opaque native target on its serial queue before unpinning its Go storage.
			session.pinned.Unpin()
			session.manager.Send(apple.Select("release"))
			session.delegate.Send(apple.Select("release"))
		})
		delete(sessions, session.delegate)
		for {
			select {
			case event := <-session.events:
				event.release()
			default:
				close(session.events)
				return
			}
		}
	}
	defer func() {
		for _, session := range sessions {
			cleanup(session)
		}
		if nativeQueue != 0 {
			appleABI.releaseQueue(nativeQueue)
		}
		for {
			select {
			case event := <-events:
				event.release()
			default:
				return
			}
		}
	}()
	initialize := func() error {
		if appleABI.err != nil {
			return appleABI.err
		}
		if nativeQueue == 0 {
			nativeQueue = appleABI.createQueue("org.netchan.bluetooth", 0)
			if nativeQueue == 0 {
				return fmt.Errorf("%w: cannot create Bluetooth callback queue", transport.ErrUnavailable)
			}
		}
		return nil
	}
	for {
		select {
		case command := <-radio.commands:
			switch request := command.(type) {
			case appleOpen:
				if stopping {
					request.reply <- appleOpenResult{err: net.ErrClosed}
					continue
				}
				if err := initialize(); err != nil {
					request.reply <- appleOpenResult{err: fmt.Errorf("%w: %w", transport.ErrUnavailable, err)}
					continue
				}
				pool := apple.Pool()
				// TCC can terminate a process that requests Bluetooth access
				// without a usage description. A permission or bundle limitation
				// must leave the node's other transports available.
				authorization := objc.Send[int64](objc.ID(objc.GetClass("CBManager")), apple.Select("authorization"))
				if authorization == 1 || authorization == 2 {
					pool()
					request.reply <- appleOpenResult{err: fmt.Errorf("%w: Bluetooth permission denied", transport.ErrUnavailable)}
					continue
				}
				if authorization == 0 {
					bundle := objc.ID(objc.GetClass("NSBundle")).Send(apple.Select("mainBundle"))
					description := bundle.Send(apple.Select("objectForInfoDictionaryKey:"), apple.String("NSBluetoothAlwaysUsageDescription"))
					if apple.Text(description) == "" {
						pool()
						request.reply <- appleOpenResult{err: fmt.Errorf("%w: Bluetooth access requires NSBluetoothAlwaysUsageDescription in the application bundle", transport.ErrUnavailable)}
						continue
					}
				}
				delegate := objc.ID(appleABI.class).Send(apple.Select("new"))
				sessionDone := make(chan struct{})
				session := &appleSession{delegate: delegate, events: make(chan appleEvent, 32), radio: radio, done: sessionDone, stop: make(chan struct{}, 1), target: &appleEventSink{events: events, overflow: overflow, done: radio.ownerDone}}
				session.pinned.Pin(session.target)
				delegate.Send(apple.Select("setNetchanSink:"), unsafe.Pointer(session.target)) // #nosec G103 -- Only an opaque pointer crosses the native boundary; cleanup detaches it on the serial queue before Unpin.
				name := "CBCentralManager"
				if request.peripheral {
					name = "CBPeripheralManager"
				}
				session.manager = objc.ID(objc.GetClass(name)).Send(apple.Select("alloc")).Send(apple.Select("initWithDelegate:queue:options:"), delegate, nativeQueue, objc.ID(0))
				go func() {
					select {
					case <-request.done:
					case <-session.stop:
					}
					close(sessionDone)
				}()
				sessions[delegate] = session
				request.reply <- appleOpenResult{session: session}
				pool()
			case appleRemove:
				cleanup(request.session)
				request.reply <- struct{}{}
				if stopping && len(sessions) == 0 {
					return
				}
			}
		case event := <-events:
			if session := sessions[event.delegate]; session != nil {
				select {
				case session.events <- event:
				case <-session.done:
					event.release()
				default:
					event.release()
					select {
					case session.stop <- struct{}{}:
					default:
					}
				}
			} else {
				event.release()
			}
		case <-overflow:
			for _, session := range sessions {
				select {
				case session.stop <- struct{}{}:
				default:
				}
			}
		case <-ownerDone:
			stopping = true
			ownerDone = nil
			if len(sessions) == 0 {
				return
			}
		}
	}
}
func (event appleEvent) release() {
	if event.first != 0 {
		event.first.Send(apple.Select("release"))
	}
	if event.second != 0 {
		event.second.Send(apple.Select("release"))
	}
	if event.delegate != 0 {
		event.delegate.Send(apple.Select("release"))
	}
}

func waitPowered(session *appleSession) error {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		state := objc.Send[int64](session.manager, apple.Select("state"))
		if state == 5 {
			return nil
		}
		if state == 2 || state == 3 || state == 4 {
			return fmt.Errorf("%w: CoreBluetooth state %d", transport.ErrUnavailable, state)
		}
		select {
		case event, open := <-session.events:
			if !open {
				return net.ErrClosed
			}
			event.release()
		case <-session.done:
			return net.ErrClosed
		case <-timer.C:
			return transport.ErrUnavailable
		}
	}
}

func (radio *appleRadio) Scan(serviceUUID string, devices chan<- string, done <-chan struct{}) error {
	session, err := radio.open(false, done)
	if err != nil {
		return err
	}
	defer session.close()
	if err = waitPowered(session); err != nil {
		return err
	}
	pool := apple.Pool()
	session.manager.Send(apple.Select("scanForPeripheralsWithServices:options:"), apple.Array(apple.UUID(serviceUUID)), objc.ID(0))
	pool()
	defer session.manager.Send(apple.Select("stopScan"))
	for {
		select {
		case event, open := <-session.events:
			if !open {
				return nil
			}
			if event.kind == "discovered" {
				identifier := apple.UUIDText(event.first.Send(apple.Select("identifier")))
				select {
				case devices <- identifier:
				case <-done:
				}
			}
			event.release()
		case <-done:
			return nil
		}
	}
}

type appleSend struct {
	payload []byte
	reply   chan error
}
type appleStop struct{}
type appleCentral struct {
	session                   *appleSession
	peripheral, write, notify objc.ID
	incoming                  chan []byte
	metadata                  []byte
	address                   string
	commands                  chan any
	done                      chan struct{}
}

func (central *appleCentral) Packets() <-chan []byte { return central.incoming }
func (central *appleCentral) Metadata() []byte       { return append([]byte(nil), central.metadata...) }
func (central *appleCentral) Address() string        { return central.address }
func (central *appleCentral) Close() error {
	select {
	case central.commands <- appleStop{}:
	case <-central.done:
	}
	<-central.done
	return nil
}
func (central *appleCentral) Send(payload []byte, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case central.commands <- appleSend{payload: append([]byte(nil), payload...), reply: reply}:
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

func (radio *appleRadio) Dial(deviceID, serviceUUID string, done <-chan struct{}) (Central, error) {
	session, err := radio.open(false, done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			session.close()
		}
	}()
	if err = waitPowered(session); err != nil {
		return nil, err
	}
	pool := apple.Pool()
	session.manager.Send(apple.Select("scanForPeripheralsWithServices:options:"), apple.Array(apple.UUID(serviceUUID)), objc.ID(0))
	pool()
	defer session.manager.Send(apple.Select("stopScan"))
	central := &appleCentral{session: session, incoming: make(chan []byte, 16), commands: make(chan any), done: make(chan struct{}), address: deviceID}
	defer func() {
		if failed && central.peripheral != 0 {
			central.peripheral.Send(apple.Select("setDelegate:"), objc.ID(0))
			session.manager.Send(apple.Select("cancelPeripheralConnection:"), central.peripheral)
			central.peripheral.Send(apple.Select("release"))
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	var info objc.ID
	var metadataRead, notifications bool
	for {
		select {
		case event, open := <-session.events:
			if !open {
				return nil, net.ErrClosed
			}
			pool := apple.Pool()
			if event.err != nil {
				event.release()
				pool()
				return nil, event.err
			}
			switch event.kind {
			case "discovered":
				if central.peripheral == 0 && strings.EqualFold(apple.UUIDText(event.first.Send(apple.Select("identifier"))), deviceID) {
					central.peripheral = event.first.Send(apple.Select("retain"))
					session.manager.Send(apple.Select("connectPeripheral:options:"), central.peripheral, objc.ID(0))
				}
			case "connected":
				central.peripheral.Send(apple.Select("setDelegate:"), session.delegate)
				central.peripheral.Send(apple.Select("discoverServices:"), apple.Array(apple.UUID(serviceUUID)))
			case "services":
				services := central.peripheral.Send(apple.Select("services"))
				for index := 0; index < apple.Count(services); index++ {
					service := apple.At(services, index)
					if strings.EqualFold(apple.UUIDText(service.Send(apple.Select("UUID"))), serviceUUID) {
						central.peripheral.Send(apple.Select("discoverCharacteristics:forService:"), objc.ID(0), service)
					}
				}
			case "characteristics":
				characteristics := event.second.Send(apple.Select("characteristics"))
				for index := 0; index < apple.Count(characteristics); index++ {
					characteristic := apple.At(characteristics, index)
					switch strings.ToLower(apple.UUIDText(characteristic.Send(apple.Select("UUID")))) {
					case UUID(serviceUUID, 1):
						central.write = characteristic
					case UUID(serviceUUID, 2):
						central.notify = characteristic
					case UUID(serviceUUID, 3):
						info = characteristic
					}
				}
				if central.write != 0 && central.notify != 0 && info != 0 {
					central.peripheral.Send(apple.Select("readValueForCharacteristic:"), info)
					central.peripheral.Send(apple.Select("setNotifyValue:forCharacteristic:"), true, central.notify)
				}
			case "value":
				if event.first == info {
					central.metadata = append([]byte(nil), event.payload...)
					metadataRead = true
				}
			case "subscribed":
				if event.first == central.notify {
					notifications = true
				}
			case "failed", "disconnected":
				event.release()
				pool()
				return nil, transport.ErrUnavailable
			}
			event.release()
			pool()
			if metadataRead && notifications {
				failed = false
				go central.run()
				return central, nil
			}
		case <-done:
			return nil, net.ErrClosed
		case <-deadline.C:
			return nil, transport.ErrUnavailable
		}
	}
}

func (central *appleCentral) run() {
	defer close(central.done)
	defer close(central.incoming)
	defer central.session.close()
	defer func() {
		central.peripheral.Send(apple.Select("setDelegate:"), objc.ID(0))
		central.session.manager.Send(apple.Select("cancelPeripheralConnection:"), central.peripheral)
		central.peripheral.Send(apple.Select("release"))
	}()
	var pending chan error
	defer func() {
		if pending != nil {
			pending <- net.ErrClosed
		}
	}()
	for {
		var commands chan any = central.commands

		select {
		case command := <-commands:
			switch request := command.(type) {
			case appleStop:
				return
			case appleSend:
				if pending != nil {
					request.reply <- transport.ErrUnavailable
					continue
				}
				pool := apple.Pool()
				pending = request.reply
				central.peripheral.Send(apple.Select("writeValue:forCharacteristic:type:"), apple.Data(request.payload), central.write, int64(0))
				pool()
			}
		case event, open := <-central.session.events:
			if !open {
				return
			}
			switch event.kind {
			case "value":
				if event.first == central.notify {
					select {
					case central.incoming <- event.payload:
					case <-central.session.done:
						event.release()
						return
					}
				}
			case "written":
				if pending != nil {
					pending <- event.err
					pending = nil
				}
			case "disconnected", "failed":
				event.release()
				return
			}
			event.release()
		case <-central.session.done:
			return
		}
	}
}

type applePeripheral struct {
	session           *appleSession
	incoming          chan Received
	commands          chan any
	done              chan struct{}
	tx, info, service objc.ID
	metadata          []byte
}

func (peripheral *applePeripheral) Packets() <-chan Received { return peripheral.incoming }
func (peripheral *applePeripheral) Address() string          { return "default" }
func (peripheral *applePeripheral) Close() error {
	select {
	case peripheral.commands <- appleStop{}:
	case <-peripheral.done:
	}
	<-peripheral.done
	return nil
}
func (peripheral *applePeripheral) Send(peer string, payload []byte, done <-chan struct{}) error {
	reply := make(chan error, 1)
	select {
	case peripheral.commands <- appleSend{payload: append([]byte(nil), payload...), reply: reply}:
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

func (radio *appleRadio) Listen(serviceUUID string, metadata []byte, done <-chan struct{}) (Peripheral, error) {
	session, err := radio.open(true, done)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			session.close()
		}
	}()
	if err = waitPowered(session); err != nil {
		return nil, err
	}
	pool := apple.Pool()
	characteristicClass := objc.ID(objc.GetClass("CBMutableCharacteristic"))
	newCharacteristic := func(uuid string, properties, permissions uint64, payload objc.ID) objc.ID {
		return characteristicClass.Send(apple.Select("alloc")).Send(apple.Select("initWithType:properties:value:permissions:"), apple.UUID(uuid), properties, payload, permissions)
	}
	rx := newCharacteristic(UUID(serviceUUID, 1), 8, 2, objc.ID(0))
	tx := newCharacteristic(UUID(serviceUUID, 2), 16, 1, objc.ID(0))
	info := newCharacteristic(UUID(serviceUUID, 3), 2, 1, apple.Data(metadata))
	service := objc.ID(objc.GetClass("CBMutableService")).Send(apple.Select("alloc")).Send(apple.Select("initWithType:primary:"), apple.UUID(serviceUUID), true)
	service.Send(apple.Select("setCharacteristics:"), apple.Array(rx, tx, info))
	session.manager.Send(apple.Select("addService:"), service)
	pool()
	defer rx.Send(apple.Select("release"))
	defer func() {
		if failed {
			session.manager.Send(apple.Select("stopAdvertising"))
			session.manager.Send(apple.Select("removeAllServices"))
			tx.Send(apple.Select("release"))
			info.Send(apple.Select("release"))
			service.Send(apple.Select("release"))
		}
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, open := <-session.events:
			if !open {
				return nil, net.ErrClosed
			}
			if event.err != nil {
				event.release()
				return nil, event.err
			}
			switch event.kind {
			case "added":
				pool := apple.Pool()
				session.manager.Send(apple.Select("startAdvertising:"), apple.Dictionary(map[string]objc.ID{"kCBAdvDataServiceUUIDs": apple.Array(apple.UUID(serviceUUID))}))
				pool()
			case "advertising":
				event.release()
				failed = false
				peripheral := &applePeripheral{session: session, incoming: make(chan Received, 32), commands: make(chan any), done: make(chan struct{}), tx: tx, info: info, service: service, metadata: append([]byte(nil), metadata...)}
				go peripheral.run()
				return peripheral, nil
			}
			event.release()
		case <-done:
			return nil, net.ErrClosed
		case <-timer.C:
			return nil, transport.ErrUnavailable
		}
	}
}

func (peripheral *applePeripheral) run() {
	defer close(peripheral.done)
	defer close(peripheral.incoming)
	defer peripheral.session.close()
	defer func() {
		peripheral.session.manager.Send(apple.Select("stopAdvertising"))
		peripheral.session.manager.Send(apple.Select("removeAllServices"))
		peripheral.tx.Send(apple.Select("release"))
		peripheral.info.Send(apple.Select("release"))
		peripheral.service.Send(apple.Select("release"))
	}()
	var pending *appleSend
	defer func() {
		if pending != nil {
			pending.reply <- net.ErrClosed
		}
	}()
	send := func(request appleSend) bool {
		pool := apple.Pool()
		ok := objc.Send[bool](peripheral.session.manager, apple.Select("updateValue:forCharacteristic:onSubscribedCentrals:"), apple.Data(request.payload), peripheral.tx, objc.ID(0))
		pool()
		return ok
	}
	for {
		var commands chan any = peripheral.commands

		select {
		case command := <-commands:
			switch request := command.(type) {
			case appleStop:
				return
			case appleSend:
				if pending != nil {
					request.reply <- transport.ErrUnavailable
					continue
				}
				if send(request) {
					request.reply <- nil
				} else {
					pending = &request
				}
			}
		case event, open := <-peripheral.session.events:
			if !open {
				return
			}
			switch event.kind {
			case "writable":
				if pending != nil && send(*pending) {
					pending.reply <- nil
					pending = nil
				}
			case "writes":
				for index := 0; index < apple.Count(event.first); index++ {
					request := apple.At(event.first, index)
					payload, err := apple.Bytes(request.Send(apple.Select("value")), 20)
					status := int64(0)
					if err != nil || objc.Send[uintptr](request, apple.Select("offset")) != 0 {
						status = 13
					} else {
						peer := apple.UUIDText(request.Send(apple.Select("central")).Send(apple.Select("identifier")))
						select {
						case peripheral.incoming <- Received{Peer: peer, Payload: payload}:
						default:
							status = 17
						}
					}
					peripheral.session.manager.Send(apple.Select("respondToRequest:withResult:"), request, status)
				}
			case "read":
				request := event.first
				offset := objc.Send[uintptr](request, apple.Select("offset"))
				status := int64(0)
				if offset > uintptr(len(peripheral.metadata)) {
					status = 7
				} else {
					pool := apple.Pool()
					request.Send(apple.Select("setValue:"), apple.Data(peripheral.metadata[offset:]))
					pool()
				}
				peripheral.session.manager.Send(apple.Select("respondToRequest:withResult:"), request, status)
			case "state":
				if objc.Send[int64](peripheral.session.manager, apple.Select("state")) != 5 {
					event.release()
					return
				}
			}
			event.release()
		case <-peripheral.session.done:
			return
		}
	}
}
