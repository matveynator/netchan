//go:build darwin && (amd64 || arm64) && !tinygo

package ble

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/matveynator/netchan/v2/pkg/transport/apple"
)

func TestAppleDelegateReuse(t *testing.T) {
	if appleABI.err != nil {
		t.Fatal(appleABI.err)
	}
	queue := appleABI.createQueue("org.netchan.test.delegates", 0)
	if queue == 0 {
		t.Fatal("cannot create native queue")
	}
	defer appleABI.releaseQueue(queue)
	for iteration := 0; iteration < 256; iteration++ {
		func() {
			events := make(chan appleEvent, 1)
			target := &appleEventSink{events: events, overflow: make(chan struct{}, 1), done: make(chan struct{})}
			var pinned runtime.Pinner
			pinned.Pin(target)
			delegate := objc.ID(appleABI.class).Send(apple.Select("new"))
			delegate.Send(apple.Select("setNetchanSink:"), unsafe.Pointer(target))
			defer func() {
				appleABI.perform(queue, func() {
					delegate.Send(apple.Select("setNetchanSink:"), unsafe.Pointer(nil))
					delegate.Send(apple.Select("centralManagerDidUpdateState:"), objc.ID(0))
					delegate.Send(apple.Select("release"))
				})
				pinned.Unpin()
				select {
				case <-events:
					t.Error("detached delegate delivered an event")
				default:
				}
			}()
			if iteration%64 == 0 {
				runtime.GC()
			}
			appleABI.perform(queue, func() { delegate.Send(apple.Select("centralManagerDidUpdateState:"), objc.ID(0)) })
			select {
			case event := <-events:
				event.release()
				if event.kind != "state" || event.delegate != delegate {
					t.Fatalf("callback routed to the wrong session: %#v", event)
				}
			default:
				t.Fatal("native callback did not reach its actor")
			}
		}()
	}
}
