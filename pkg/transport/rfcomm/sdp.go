package rfcomm

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

// SDP is a packet protocol. Limits apply before allocation and across
// continuation responses, including records supplied by unauthenticated radios.
func readDescription(uuid string, exchange func([]byte) ([]byte, error)) (string, error) {
	service, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	if err != nil || len(service) != 16 {
		return "", errors.New("netchan: invalid SDP UUID")
	}
	var continuation, attributes []byte
	for transaction := uint16(1); transaction <= 16; transaction++ {
		if len(continuation) > 16 {
			return "", errors.New("netchan: invalid SDP continuation")
		}
		parameters := append([]byte{0x35, 17, 0x1c}, service...)
		parameters = append(parameters, 0x20, 0x00, 0x35, 3, 0x09, 0x01, 0x01, byte(len(continuation)&31))
		parameters = append(parameters, continuation...)
		request := make([]byte, 5, 5+len(parameters))
		if len(parameters) > 65535 {
			return "", errors.New("netchan: oversized SDP request")
		}
		request[0] = 6
		binary.BigEndian.PutUint16(request[1:3], transaction)
		binary.BigEndian.PutUint16(request[3:5], uint16(len(parameters)&65535))
		response, exchangeErr := exchange(append(request, parameters...))
		if exchangeErr != nil {
			return "", exchangeErr
		}
		if len(response) < 8 || len(response) > 16384 || response[0] != 7 || binary.BigEndian.Uint16(response[1:3]) != transaction || int(binary.BigEndian.Uint16(response[3:5])) != len(response)-5 {
			return "", errors.New("netchan: invalid SDP response")
		}
		length := int(binary.BigEndian.Uint16(response[5:7]))
		if length > len(response)-8 || len(attributes)+length > 16384 {
			return "", errors.New("netchan: oversized SDP attributes")
		}
		attributes = append(attributes, response[7:7+length]...)
		remaining := response[7+length:]
		if int(remaining[0]) != len(remaining)-1 || remaining[0] > 16 {
			return "", errors.New("netchan: invalid SDP continuation")
		}
		if remaining[0] == 0 {
			return descriptionAttribute(attributes, 0)
		}
		continuation = append([]byte(nil), remaining[1:]...)
	}
	return "", errors.New("netchan: SDP continuation limit exceeded")
}

func element(encoded []byte) (kind byte, payload, rest []byte, err error) {
	if len(encoded) == 0 {
		return 0, nil, nil, errors.New("netchan: missing SDP element")
	}
	kind, size := encoded[0]>>3, encoded[0]&7
	encoded = encoded[1:]
	length := uint64(1) << size
	if kind == 0 {
		length = 0
	} else if size >= 5 {
		width := 1 << (size - 5)
		if len(encoded) < width {
			return 0, nil, nil, errors.New("netchan: truncated SDP length")
		}
		length = 0
		for _, part := range encoded[:width] {
			length = length<<8 | uint64(part)
		}
		encoded = encoded[width:]
	}
	if length > uint64(len(encoded)) || length > 16384 {
		return 0, nil, nil, errors.New("netchan: truncated SDP element")
	}
	return kind, encoded[:int(length)], encoded[int(length):], nil
}

func descriptionAttribute(encoded []byte, depth int) (string, error) {
	if depth > 8 {
		return "", errors.New("netchan: SDP nesting limit exceeded")
	}
	for len(encoded) > 0 {
		kind, payload, rest, err := element(encoded)
		if err != nil {
			return "", err
		}
		encoded = rest
		if kind == 1 && len(payload) == 2 && binary.BigEndian.Uint16(payload) == 0x0101 {
			kind, payload, _, err = element(rest)
			if err != nil {
				return "", err
			}
			if kind != 4 || len(payload) > 320 {
				return "", errors.New("netchan: invalid SDP description")
			}
			return string(payload), nil
		}
		if kind == 6 {
			description, nestedErr := descriptionAttribute(payload, depth+1)
			if nestedErr != nil {
				return "", nestedErr
			}
			if description != "" {
				return description, nil
			}
		}
	}
	return "", nil
}
