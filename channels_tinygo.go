//go:build tinygo

package netchan

import (
	"fmt"
	"reflect"
)

// Missing reflection operations affect channel bindings, not transport support.
const dynamicChannelsAvailable = false

func makeDynamicChannel(element reflect.Type, capacity int) (reflect.Value, error) {
	return reflect.Value{}, fmt.Errorf("%w: %s requires a ChannelElement binding", ErrUnsupportedType, element)
}
