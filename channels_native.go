//go:build !tinygo

package netchan

import "reflect"

// The ordinary compiler supports a dynamic fallback for unregistered types.
const dynamicChannelsAvailable = true

func makeDynamicChannel(element reflect.Type, capacity int) (reflect.Value, error) {
	return reflect.MakeChan(reflect.ChanOf(reflect.BothDir, element), capacity), nil
}
