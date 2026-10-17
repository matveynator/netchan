package ble

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"time"

	"github.com/matveynator/netchan/v2/pkg/transport"
)

// Discovery records are untrusted hints. Their full fingerprint and instance
// must fit even when a controller only supports 20-byte notifications/reads.
func readPeerMetadata(central Central, done <-chan struct{}) ([]byte, error) {
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	identifier := binary.BigEndian.Uint32(nonce[:])
	if identifier == 0 {
		identifier = 1
	}
	canceled, finished, watcherDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	timer := time.NewTimer(10 * time.Second)
	go func() {
		defer close(watcherDone)
		defer close(canceled)
		select {
		case <-done:
		case <-timer.C:
		case <-finished:
		}
	}()
	defer func() { close(finished); timer.Stop(); <-watcherDone }()
	request := envelope(identifier, messageOpen, []byte{metadataMarker})
	if err := central.Send(request, canceled); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var pieces [9][]byte
	for {
		select {
		case encoded, open := <-central.Packets():
			if !open {
				return nil, net.ErrClosed
			}
			if len(encoded) < envelopeSize+2 || len(encoded) > 20 || binary.BigEndian.Uint32(encoded[:4]) != identifier || encoded[4]&3 != messageReady {
				continue
			}
			length := envelopeSize + int(encoded[4]>>2)
			if length > len(encoded) || length < envelopeSize+2 || encoded[5] != metadataMarker {
				continue
			}
			index := int(encoded[6])
			if index < 0 || index >= len(pieces) {
				continue
			}
			pieces[index] = append([]byte(nil), encoded[7:length]...)
			if len(pieces[0]) < 2 || pieces[0][0] != 2 || pieces[0][1] == 0 || pieces[0][1] > 63 {
				continue
			}
			wanted := 50 + int(pieces[0][1])
			count := (wanted + metadataPartSize - 1) / metadataPartSize
			complete := true
			for part := 0; part < count; part++ {
				expected := metadataPartSize
				if part == count-1 {
					expected = wanted - part*metadataPartSize
				}
				if len(pieces[part]) != expected {
					complete = false
					break
				}
			}
			if complete {
				metadata := make([]byte, 0, wanted)
				for _, part := range pieces[:count] {
					metadata = append(metadata, part...)
				}
				return metadata, nil
			}
		case <-ticker.C:
			if err := central.Send(request, canceled); err != nil {
				return nil, err
			}
		case <-canceled:
			return nil, transport.ErrUnavailable
		}
	}
}
