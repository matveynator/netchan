package rfcomm

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestSDPDescriptionAcrossContinuation(t *testing.T) {
	want := metadata("tasks", strings.Repeat("a", 64), strings.Repeat("b", 32))
	attributes := append([]byte{0x09, 1, 1, 0x25, byte(len(want))}, want...)
	attributes = append([]byte{0x35, byte(len(attributes))}, attributes...)
	attributes = append([]byte{0x35, byte(len(attributes))}, attributes...)
	first := true
	got, err := readDescription(UUID("tasks"), func(request []byte) ([]byte, error) {
		part := attributes[12:]
		continuation := []byte{0}
		if first {
			part = attributes[:12]
			continuation = []byte{1, 42}
			first = false
		} else if request[len(request)-1] != 42 {
			t.Fatal("continuation not returned")
		}
		response := []byte{7, request[1], request[2], 0, 0, 0, 0}
		binary.BigEndian.PutUint16(response[5:7], uint16(len(part)))
		response = append(append(response, part...), continuation...)
		binary.BigEndian.PutUint16(response[3:5], uint16(len(response)-5))
		return response, nil
	})
	if err != nil || got != want {
		t.Fatalf("description = %q, %v", got, err)
	}
}
func FuzzSDPDescription(f *testing.F) {
	f.Add([]byte{0x35, 0})
	f.Add([]byte{0x36, 255, 255})
	f.Add([]byte{0x09, 1, 1, 0x25, 0})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 16384 {
			return
		}
		description, err := descriptionAttribute(encoded, 0)
		if err == nil && len(description) > 320 {
			t.Fatal("unbounded SDP description")
		}
	})
}
func FuzzSDPMetadata(f *testing.F) {
	f.Add(metadata("tasks", strings.Repeat("a", 64), strings.Repeat("b", 32)))
	f.Fuzz(func(t *testing.T, encoded string) {
		service, peer, instance, err := parseMetadata(encoded)
		if err == nil {
			if _, _, _, err = parseMetadata(metadata(service, peer, instance)); err != nil {
				t.Fatal(err)
			}
		}
	})
}
