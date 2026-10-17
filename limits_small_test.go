//go:build netchan_small || baremetal

package netchan

import (
	"errors"
	"reflect"
	"testing"
)

func TestSmallDecoderRejectsAllocationBeforeReadingElements(t *testing.T) {
	for _, element := range []any{[]uint64{}, []struct{}{}} {
		length := maximumCollectionLength
		if reflect.TypeOf(element).Elem().Size() == 0 {
			length++
		}
		encoded := wireWriter{}
		encoded.byte(1)
		encoded.integer(length, "slice length")
		_, err := prepareDecodedChannelValue(nil, reflect.TypeOf(element), encoded.bytes)
		if !errors.Is(err, ErrPayloadLimit) {
			t.Fatalf("oversized %T allocation = %v", element, err)
		}
	}
}
