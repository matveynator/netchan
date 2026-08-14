// Package netchan extends Go channel communication across process and machine boundaries.
//
// # File organization policy
//
// This file is intentionally organized as one top-to-bottom implementation split
// into explicit logical blocks. Every new type, constant, variable, or function
// must be added to the block that owns its architectural responsibility. Related
// declarations stay next to each other inside that block. A change that introduces
// a genuinely separate responsibility must add a new block and update the index
// below; it must not turn an existing block into a general-purpose manager.
//
// Every block starts with "BEGIN: <name>" and ends with the matching
// "END: <name>" marker. Keep both markers intact when editing or moving code.
// Public entry points should appear before their private processes and helpers so
// each subsystem remains readable from its boundary toward its implementation.
// Communication between subsystems must remain explicit through channels and
// session commands rather than shared mutable state.
//
// Logical block index
//
//  1. Version 2 protocol model
//     Wire constants, frame types, identifiers, directions, and protocol errors.
//  2. Version 2 binary wire framing
//     Explicit length-prefixed encoders, decoders, and bounded binary helpers.
//  3. Public native channel facade
//     Listen, Dial, native channel directions, configuration, and strict Deliver.
//  4. Node, listener, and session discovery
//     Internal node configuration, listeners, connection setup, and registries.
//  5. Logical session actor and reconnection
//     Session commands, sequencing, acknowledgements, leases, and reattachment.
//  6. Native channel bridges and channel capabilities
//     Root bridges, directional capability transfer, and binary payload coding.
package netchan

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"reflect"
	"time"
)

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Version 2 protocol model
////////////////////////////////////////////////////////////////////////////////

const (
	protocolVersion             = 2
	maximumPendingFrames        = 1024
	channelFrameBufferSize      = maximumPendingFrames + 32
	maximumValueCapabilities    = 16
	maximumSessionCapabilities  = 256
	maximumSessionQueuedBytes   = 64 << 20
	maximumSessionPreparedBytes = 64 << 20
	maximumSessionDecodedBytes  = 64 << 20
	maximumCapabilityBytes      = 64 << 20
	maximumCapabilityRouteBytes = 64 << 20
	maximumNodeSessions         = 1024
	maximumConcurrentHandshakes = 64
	maximumConcurrentEncodings  = 4
	maximumCapabilityTombstones = maximumSessionCapabilities
	defaultReconnectDelay       = 250 * time.Millisecond
	maximumReconnectDelay       = 5 * time.Second
	heartbeatInterval           = 10 * time.Second
	heartbeatTimeout            = 30 * time.Second
	disconnectedLease           = 5 * time.Minute
	rootOpenTimeout             = 15 * time.Second
	sessionShutdownTimeout      = 15 * time.Second
)

type frameKind uint8

const (
	frameHello frameKind = iota + 1
	frameHelloResume
	frameHelloAccepted
	frameSessionUnknown
	frameSessionRejected
	frameAcknowledgement
	frameOpenChannel
	frameChannelOpened
	frameChannelValue
	frameChannelPrepared
	frameChannelDelivered
	frameChannelReleased
	frameChannelClosed
	frameChannelRevoked
	frameChannelForgotten
	frameSessionClosed
	frameHeartbeat
)

type networkFrame struct {
	Version   uint16
	Kind      frameKind
	SessionID string
	Sequence  uint64
	Ack       uint64

	ChannelID  string
	TransferID string
	Name       string
	Direction  channelDirection
	Capacity   int
	Schema     schemaHash

	Payload   []byte
	Error     string
	ErrorCode protocolErrorCode
}

type channelDirection uint8

type schemaHash [sha256.Size]byte

type protocolErrorCode uint8

const (
	protocolErrorGeneral protocolErrorCode = iota
	protocolErrorUnsupportedType
	protocolErrorSchemaMismatch
	protocolErrorMalformedFrame
	protocolErrorPayloadLimit
	protocolErrorChannelClosed
)

const (
	channelBidirectional channelDirection = iota + 1
	channelSendOnly
	channelReceiveOnly
)

func randomIdentifier() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return hex.EncodeToString(identifier), nil
}

type physicalConnection struct {
	connection net.Conn
	outgoing   chan physicalWriteCommand
	done       chan struct{}
}

type physicalWriteCommand struct {
	frame    networkFrame
	written  chan error
	deadline time.Time
}

type physicalConnectionEvent struct {
	connection *physicalConnection
	frame      networkFrame
	err        error
}

func runPhysicalConnection(connection net.Conn, events chan<- physicalConnectionEvent, sessionDone, ownerDone <-chan struct{}) *physicalConnection {
	physical := &physicalConnection{
		connection: connection,
		outgoing:   make(chan physicalWriteCommand),
		done:       make(chan struct{}),
	}
	failures := make(chan error, 2)

	reportFailure := func(err error) {
		select {
		case failures <- err:
		case <-physical.done:
		}
	}

	go func() {
		var err error
		report := false
		select {
		case err = <-failures:
			report = true
		case <-sessionDone:
		case <-ownerDone:
			shutdownTimer := time.NewTimer(sessionShutdownTimeout)
			select {
			case err = <-failures:
				report = true
			case <-sessionDone:
			case <-shutdownTimer.C:
			}
			shutdownTimer.Stop()
		}
		_ = connection.Close()
		close(physical.done)
		if !report {
			return
		}
		select {
		case events <- physicalConnectionEvent{connection: physical, err: err}:
		case <-sessionDone:
		}
	}()

	go func() {
		decoder := newFrameDecoder(connection)
		for {
			frame, err := decoder.decode()
			if err != nil {
				reportFailure(err)
				return
			}
			select {
			case events <- physicalConnectionEvent{connection: physical, frame: frame}:
			case <-physical.done:
				return
			case <-sessionDone:
				return
			}
		}
	}()

	go func() {
		encoder := newFrameEncoder(connection)
		for {
			var command physicalWriteCommand
			select {
			case command = <-physical.outgoing:
			case <-physical.done:
				return
			}
			deadline := command.deadline
			if deadline.IsZero() {
				deadline = time.Now().Add(15 * time.Second)
			}
			if err := connection.SetWriteDeadline(deadline); err != nil {
				if command.written != nil {
					command.written <- err
				}
				reportFailure(err)
				return
			}
			if err := encoder.encode(command.frame); err != nil {
				if command.written != nil {
					command.written <- err
				}
				reportFailure(err)
				return
			}
			if command.written != nil {
				command.written <- nil
			}
		}
	}()

	return physical
}

func (physical *physicalConnection) send(frame networkFrame) bool {
	select {
	case physical.outgoing <- physicalWriteCommand{frame: frame}:
		return true
	case <-physical.done:
		return false
	}
}

func (physical *physicalConnection) sendAndWait(frame networkFrame) error {
	return physical.sendAndWaitUntil(frame, time.Time{})
}

func (physical *physicalConnection) sendAndWaitUntil(frame networkFrame, deadline time.Time) error {
	var deadlineExpired <-chan time.Time
	var deadlineTimer *time.Timer
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = physical.connection.Close()
			return os.ErrDeadlineExceeded
		}
		deadlineTimer = time.NewTimer(remaining)
		deadlineExpired = deadlineTimer.C
		defer deadlineTimer.Stop()
	}
	written := make(chan error, 1)
	select {
	case physical.outgoing <- physicalWriteCommand{frame: frame, written: written, deadline: deadline}:
	case <-physical.done:
		return net.ErrClosed
	case <-deadlineExpired:
		_ = physical.connection.Close()
		return os.ErrDeadlineExceeded
	}
	select {
	case err := <-written:
		return err
	case <-physical.done:
		return net.ErrClosed
	case <-deadlineExpired:
		_ = physical.connection.Close()
		return os.ErrDeadlineExceeded
	}
}

// Session termination is appended behind every actor-owned wire frame. Waiting
// on each write keeps terminal control frames from being overtaken by the
// unsequenced SESSION_CLOSED boundary.
func flushSessionClose(physical *physicalConnection, queued []networkFrame, sessionID string) {
	if physical == nil {
		return
	}
	deadline := time.Now().Add(sessionShutdownTimeout)
	for _, frame := range queued {
		if err := physical.sendAndWaitUntil(frame, deadline); err != nil {
			return
		}
	}
	_ = physical.sendAndWaitUntil(networkFrame{Version: protocolVersion, Kind: frameSessionClosed, SessionID: sessionID}, deadline)
}

var errSessionClosed = errors.New("netchan: session closed")

var (
	// ErrChannelClosed reports that no further rendezvous can complete.
	ErrChannelClosed = errors.New("netchan: channel closed")
	// ErrUnsupportedType reports a Go type without an unambiguous NetChan wire representation.
	ErrUnsupportedType = errors.New("netchan: unsupported network value type")
	// ErrSchemaMismatch reports peers opening one channel with different concrete Go types.
	ErrSchemaMismatch = errors.New("netchan: channel schema mismatch")
	// ErrMalformedFrame reports invalid or inconsistent protocol data from a peer.
	ErrMalformedFrame = errors.New("netchan: malformed network frame")
	// ErrPayloadLimit reports a value which exceeds a bounded decoder or wire limit.
	ErrPayloadLimit = errors.New("netchan: payload limit exceeded")
	// ErrSessionExpired reports a logical session which the peer can no longer resume.
	ErrSessionExpired = errors.New("netchan: logical session expired")
	// ErrSessionRejected reports that a peer cannot admit another logical session.
	ErrSessionRejected = errors.New("netchan: logical session rejected")
)

func validateHandshake(frame networkFrame, expectedKind frameKind) error {
	if frame.Kind != expectedKind {
		return fmt.Errorf("%w: unexpected handshake frame %d", ErrMalformedFrame, frame.Kind)
	}
	if frame.Version != protocolVersion {
		return fmt.Errorf("%w: protocol version %d is not supported", ErrMalformedFrame, frame.Version)
	}
	if frame.SessionID == "" {
		return fmt.Errorf("%w: handshake has no session identifier", ErrMalformedFrame)
	}
	if frame.Sequence != 0 || frame.Ack != 0 {
		return fmt.Errorf("%w: handshake contains session sequencing fields", ErrMalformedFrame)
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// END: Version 2 protocol model
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Version 2 binary wire framing
////////////////////////////////////////////////////////////////////////////////

const (
	maximumWireFrameSize      = 16 << 20
	maximumChannelPayloadSize = maximumWireFrameSize - 1024
	maximumHandshakeFrameSize = 1024
)

const (
	maximumWireIdentifierSize = 64
	maximumWireNameSize       = 1024
	maximumWireErrorSize      = 4096
)

type binaryFrameEncoder struct {
	writer *bufio.Writer
}

func newFrameEncoder(writer io.Writer) *binaryFrameEncoder {
	return &binaryFrameEncoder{writer: bufio.NewWriter(writer)}
}

func (encoder *binaryFrameEncoder) encode(frame networkFrame) error {
	payload, err := encodeNetworkFrame(frame)
	if err != nil {
		return err
	}
	if len(payload) > maximumWireFrameSize {
		return fmt.Errorf("%w: frame exceeds maximum size", ErrPayloadLimit)
	}

	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload))) // #nosec G115 -- the frame limit above is smaller than uint32.
	if _, err := encoder.writer.Write(size[:]); err != nil {
		return err
	}
	if _, err := encoder.writer.Write(payload); err != nil {
		return err
	}
	return encoder.writer.Flush()
}

type binaryFrameDecoder struct {
	reader           io.Reader
	maximumFrameSize uint32
}

func newFrameDecoder(reader io.Reader) *binaryFrameDecoder {
	return &binaryFrameDecoder{reader: reader, maximumFrameSize: maximumWireFrameSize}
}

func newHandshakeFrameDecoder(reader io.Reader) *binaryFrameDecoder {
	return &binaryFrameDecoder{reader: reader, maximumFrameSize: maximumHandshakeFrameSize}
}

func (decoder *binaryFrameDecoder) decode() (networkFrame, error) {
	var size [4]byte
	if _, err := io.ReadFull(decoder.reader, size[:]); err != nil {
		return networkFrame{}, err
	}
	frameSize := binary.BigEndian.Uint32(size[:])
	if frameSize == 0 || frameSize > decoder.maximumFrameSize {
		return networkFrame{}, fmt.Errorf("%w: invalid frame size", ErrMalformedFrame)
	}

	payload := make([]byte, frameSize)
	if _, err := io.ReadFull(decoder.reader, payload); err != nil {
		return networkFrame{}, err
	}

	frame, err := decodeNetworkFrame(payload)
	if err != nil {
		return networkFrame{}, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	return frame, nil
}

func encodeNetworkFrame(frame networkFrame) ([]byte, error) {
	if err := validateNetworkFrame(frame); err != nil {
		return nil, err
	}
	writer := wireWriter{bytes: make([]byte, 0, 128+len(frame.Payload))}
	writer.uint16(frame.Version)
	writer.byte(byte(frame.Kind))
	writer.string(frame.SessionID, maximumWireIdentifierSize, "session identifier")
	writer.uint64(frame.Sequence)
	writer.uint64(frame.Ack)

	switch frame.Kind {
	case frameHello, frameHelloResume, frameHelloAccepted, frameSessionUnknown, frameSessionRejected, frameAcknowledgement, frameSessionClosed, frameHeartbeat:
	case frameOpenChannel:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
		writer.string(frame.Name, maximumWireNameSize, "channel name")
		writer.byte(byte(frame.Direction))
		writer.integer(frame.Capacity, "channel capacity")
		writer.bytes = append(writer.bytes, frame.Schema[:]...)
	case frameChannelOpened:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
	case frameChannelValue:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
		writer.string(frame.TransferID, maximumWireIdentifierSize, "transfer identifier")
		writer.data(frame.Payload, maximumWireFrameSize, "channel payload")
	case frameChannelPrepared, frameChannelDelivered, frameChannelReleased:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
		writer.string(frame.TransferID, maximumWireIdentifierSize, "transfer identifier")
	case frameChannelClosed, frameChannelForgotten:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
	case frameChannelRevoked:
		writer.string(frame.ChannelID, maximumWireIdentifierSize, "channel identifier")
		writer.byte(byte(frame.ErrorCode))
		writer.string(frame.Error, maximumWireErrorSize, "protocol error")
	default:
		writer.fail(fmt.Errorf("netchan: unknown frame kind %d", frame.Kind))
	}
	return writer.result()
}

func decodeNetworkFrame(payload []byte) (networkFrame, error) {
	reader := wireReader{bytes: payload}
	frame := networkFrame{
		Version:   reader.uint16(),
		Kind:      frameKind(reader.byte()),
		SessionID: reader.string(maximumWireIdentifierSize, "session identifier"),
		Sequence:  reader.uint64(),
		Ack:       reader.uint64(),
	}

	switch frame.Kind {
	case frameHello, frameHelloResume, frameHelloAccepted, frameSessionUnknown, frameSessionRejected, frameAcknowledgement, frameSessionClosed, frameHeartbeat:
	case frameOpenChannel:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
		frame.Name = reader.string(maximumWireNameSize, "channel name")
		frame.Direction = channelDirection(reader.byte())
		frame.Capacity = reader.integer("channel capacity")
		copy(frame.Schema[:], reader.take(sha256.Size, "channel schema"))
	case frameChannelOpened:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
	case frameChannelValue:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
		frame.TransferID = reader.string(maximumWireIdentifierSize, "transfer identifier")
		frame.Payload = reader.data(maximumWireFrameSize, "channel payload")
	case frameChannelPrepared, frameChannelDelivered, frameChannelReleased:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
		frame.TransferID = reader.string(maximumWireIdentifierSize, "transfer identifier")
	case frameChannelClosed, frameChannelForgotten:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
	case frameChannelRevoked:
		frame.ChannelID = reader.string(maximumWireIdentifierSize, "channel identifier")
		frame.ErrorCode = protocolErrorCode(reader.byte())
		frame.Error = reader.string(maximumWireErrorSize, "protocol error")
	default:
		reader.fail(fmt.Errorf("netchan: unknown frame kind %d", frame.Kind))
	}
	if err := reader.finish(); err != nil {
		return networkFrame{}, err
	}
	if err := validateNetworkFrame(frame); err != nil {
		return networkFrame{}, err
	}
	return frame, nil
}

func validateNetworkFrame(frame networkFrame) error {
	if frame.Version != protocolVersion {
		return fmt.Errorf("netchan: protocol version %d is not supported", frame.Version)
	}
	if frame.SessionID == "" {
		return errors.New("netchan: frame has no session identifier")
	}
	switch frame.Kind {
	case frameHello, frameHelloResume, frameHelloAccepted, frameSessionUnknown, frameSessionRejected, frameAcknowledgement, frameSessionClosed, frameHeartbeat:
		return nil
	case frameOpenChannel:
		if frame.ChannelID == "" || frame.Name == "" {
			return errors.New("netchan: open-channel frame is incomplete")
		}
		if !validChannelDirection(frame.Direction) || frame.Capacity < 0 {
			return errors.New("netchan: open-channel frame has invalid channel properties")
		}
	case frameChannelOpened, frameChannelValue, frameChannelPrepared, frameChannelDelivered, frameChannelReleased, frameChannelClosed, frameChannelRevoked, frameChannelForgotten:
		if frame.ChannelID == "" {
			return errors.New("netchan: channel frame has no channel identifier")
		}
		if (frame.Kind == frameChannelValue || frame.Kind == frameChannelPrepared || frame.Kind == frameChannelDelivered || frame.Kind == frameChannelReleased) && frame.TransferID == "" {
			return errors.New("netchan: transfer frame has no transfer identifier")
		}
		if frame.Kind == frameChannelRevoked && frame.ErrorCode > protocolErrorChannelClosed {
			return errors.New("netchan: channel revoke has an invalid error code")
		}
		if frame.Kind == frameChannelValue && len(frame.Payload) > maximumChannelPayloadSize {
			return fmt.Errorf("%w: channel value exceeds maximum wire size", ErrPayloadLimit)
		}
	default:
		return fmt.Errorf("netchan: unknown frame kind %d", frame.Kind)
	}
	return nil
}

func validChannelDirection(direction channelDirection) bool {
	return direction == channelBidirectional || direction == channelSendOnly || direction == channelReceiveOnly
}

type wireWriter struct {
	bytes []byte
	err   error
}

func (writer *wireWriter) fail(err error) {
	if writer.err == nil {
		writer.err = err
	}
}

func (writer *wireWriter) byte(value byte) {
	writer.bytes = append(writer.bytes, value)
}

func (writer *wireWriter) uint16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	writer.bytes = append(writer.bytes, encoded[:]...)
}

func (writer *wireWriter) uint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	writer.bytes = append(writer.bytes, encoded[:]...)
}

func (writer *wireWriter) uint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writer.bytes = append(writer.bytes, encoded[:]...)
}

func (writer *wireWriter) integer(value int, name string) {
	if value < 0 || uint64(value) > uint64(^uint32(0)) {
		writer.fail(fmt.Errorf("netchan: %s is out of range", name))
		return
	}
	writer.uint32(uint32(value))
}

func (writer *wireWriter) string(value string, maximum int, name string) {
	if len(value) > maximum || len(value) > int(^uint16(0)) {
		writer.fail(fmt.Errorf("netchan: %s exceeds maximum size", name))
		return
	}
	writer.uint16(uint16(len(value))) // #nosec G115 -- both maximum and uint16 bounds are checked above.
	writer.bytes = append(writer.bytes, value...)
}

func (writer *wireWriter) data(value []byte, maximum int, name string) {
	if len(value) > maximum || uint64(len(value)) > uint64(^uint32(0)) {
		writer.fail(fmt.Errorf("%w: %s exceeds maximum size", ErrPayloadLimit, name))
		return
	}
	writer.uint32(uint32(len(value))) // #nosec G115 -- both maximum and uint32 bounds are checked above.
	writer.bytes = append(writer.bytes, value...)
}

func (writer *wireWriter) result() ([]byte, error) {
	if writer.err != nil {
		return nil, writer.err
	}
	return writer.bytes, nil
}

type wireReader struct {
	bytes  []byte
	offset int
	err    error
}

func (reader *wireReader) fail(err error) {
	if reader.err == nil {
		reader.err = err
	}
}

func (reader *wireReader) take(size int, name string) []byte {
	if reader.err != nil {
		return nil
	}
	if size < 0 || size > len(reader.bytes)-reader.offset {
		reader.fail(fmt.Errorf("netchan: truncated %s", name))
		return nil
	}
	value := reader.bytes[reader.offset : reader.offset+size]
	reader.offset += size
	return value
}

func (reader *wireReader) byte() byte {
	value := reader.take(1, "byte")
	if len(value) == 0 {
		return 0
	}
	return value[0]
}

func (reader *wireReader) uint16() uint16 {
	value := reader.take(2, "uint16")
	if len(value) != 2 {
		return 0
	}
	return binary.BigEndian.Uint16(value)
}

func (reader *wireReader) uint32() uint32 {
	value := reader.take(4, "uint32")
	if len(value) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32(value)
}

func (reader *wireReader) uint64() uint64 {
	value := reader.take(8, "uint64")
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}

func (reader *wireReader) integer(name string) int {
	value := reader.uint32()
	converted := int(value)
	if converted < 0 || uint64(converted) != uint64(value) {
		reader.fail(fmt.Errorf("netchan: %s is out of range", name))
		return 0
	}
	return converted
}

func (reader *wireReader) string(maximum int, name string) string {
	size := int(reader.uint16())
	if size > maximum {
		reader.fail(fmt.Errorf("netchan: %s exceeds maximum size", name))
		return ""
	}
	return string(reader.take(size, name))
}

func (reader *wireReader) data(maximum int, name string) []byte {
	size := uint64(reader.uint32())
	if size > uint64(maximum) || size > uint64(len(reader.bytes)-reader.offset) { // #nosec G115 -- maximum and remaining length are non-negative protocol bounds.
		reader.fail(fmt.Errorf("netchan: invalid %s size", name))
		return nil
	}
	value := reader.take(int(size), name) // #nosec G115 -- size is bounded by the remaining in-memory frame above.
	return append([]byte(nil), value...)
}

func (reader *wireReader) finish() error {
	if reader.err != nil {
		return reader.err
	}
	if reader.offset != len(reader.bytes) {
		return errors.New("netchan: frame has trailing bytes")
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// END: Version 2 binary wire framing
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Public native channel facade
////////////////////////////////////////////////////////////////////////////////

const rootChannelName = "netchan.root"

// Config contains the boundary settings for Listen and Dial. TLS is required
// so peer authentication cannot be omitted accidentally. Capacity applies only
// to Channel.Send; Channel.Receive is intentionally unbuffered so Deliver can
// observe the remote application's actual receive operation.
type Config struct {
	Capacity int
	TLS      *tls.Config
}

type deliveryRequest[T any] struct {
	message T
	reply   chan error
}

// Channel exposes the network boundary as ordinary directional Go channels.
// Keeping the directions separate prevents local senders and receivers from
// rendezvousing with each other and accidentally bypassing the network.
type Channel[T any] struct {
	Send    chan<- T
	Receive <-chan T
	Errors  <-chan error
	Done    <-chan struct{}

	outgoing     chan T
	incoming     chan T
	deliveries   chan deliveryRequest[T]
	errors       chan error
	done         chan struct{}
	abortSession func() error
}

func newChannel[T any](capacity int) *Channel[T] {
	outgoing := make(chan T, capacity)
	incoming := make(chan T)
	errorsChannel := make(chan error, 32)
	done := make(chan struct{})
	return &Channel[T]{
		Send:       outgoing,
		Receive:    incoming,
		Errors:     errorsChannel,
		Done:       done,
		outgoing:   outgoing,
		incoming:   incoming,
		deliveries: make(chan deliveryRequest[T]),
		errors:     errorsChannel,
		done:       done,
	}
}

// Deliver transfers one value and returns only after the remote application has
// received it. Ordinary sends transfer ownership to the local NetChan process;
// Deliver is the explicit network-wide rendezvous operation.
func (channel *Channel[T]) Deliver(message T) error {
	if channel == nil || channel.done == nil {
		return ErrChannelClosed
	}
	reply := make(chan error, 1)
	select {
	case channel.deliveries <- deliveryRequest[T]{message: message, reply: reply}:
	case <-channel.done:
		return ErrChannelClosed
	}
	err, open := <-reply
	if !open && err == nil {
		return ErrChannelClosed
	}
	return err
}

// Close closes the local send direction, lets already accepted values drain,
// and waits until the logical network channel has terminated.
func (channel *Channel[T]) Close() error {
	if channel == nil || channel.done == nil {
		return nil
	}
	closeOwnedChannel(channel.outgoing)
	<-channel.done
	return nil
}

// Abort cancels queued work immediately and terminates the logical channel.
// It is the explicit escape hatch when graceful Close must not wait for a peer.
func (channel *Channel[T]) Abort() error {
	if channel == nil || channel.done == nil {
		return nil
	}
	if channel.abortSession != nil {
		_ = channel.abortSession()
	}
	<-channel.done
	return nil
}

func closeOwnedChannel[T any](channel chan T) {
	defer func() { _ = recover() }()
	close(channel)
}

// Listener accepts independent logical clients. Each client owns one
// bidirectional Channel in Channels and retains it across physical reconnects.
type Listener[T any] struct {
	Channels <-chan *Channel[T]
	Errors   <-chan error
	Done     <-chan struct{}
	Address  string

	node *node
}

// Close stops accepting clients and closes every logical session.
func (listener *Listener[T]) Close() error {
	if listener == nil || listener.node == nil {
		return nil
	}
	return listener.node.Close()
}

// Listen starts an authenticated TLS listener for channels carrying T.
func Listen[T any](address string, configurations ...Config) (*Listener[T], error) {
	configuration, err := normalizeConfig(configurations)
	if err != nil {
		return nil, err
	}
	elementType := reflect.TypeOf((*T)(nil)).Elem()
	if err := validateNetworkType(elementType); err != nil {
		return nil, err
	}
	if err := validateChannelCapacity(configuration.Capacity, elementType); err != nil {
		return nil, err
	}
	serverTLS, err := roleTLSConfiguration(configuration.TLS)
	if err != nil {
		return nil, err
	}
	node, err := newNode(withTLSConfigs(serverTLS, unusedTLSConfiguration()))
	if err != nil {
		return nil, err
	}
	accepted := make(chan *Channel[T])
	if err := publishRoot(node, accepted, configuration.Capacity); err != nil {
		_ = node.Close()
		return nil, err
	}
	networkListener, err := node.listen(address)
	if err != nil {
		_ = node.Close()
		return nil, err
	}
	errorsChannel := make(chan error, 32)
	done := make(chan struct{})
	listener := &Listener[T]{Channels: accepted, Errors: errorsChannel, Done: done, Address: networkListener.Addr().String(), node: node}
	go forwardListenerLifecycle(node, accepted, errorsChannel, done)
	return listener, nil
}

// Dial establishes one logical channel and reconnects its physical TLS
// connection after transient failures.
func Dial[T any](address string, configurations ...Config) (*Channel[T], error) {
	configuration, err := normalizeConfig(configurations)
	if err != nil {
		return nil, err
	}
	elementType := reflect.TypeOf((*T)(nil)).Elem()
	if err := validateNetworkType(elementType); err != nil {
		return nil, err
	}
	if err := validateChannelCapacity(configuration.Capacity, elementType); err != nil {
		return nil, err
	}
	clientTLS, err := roleTLSConfiguration(configuration.TLS)
	if err != nil {
		return nil, err
	}
	node, err := newNode(withTLSConfigs(unusedTLSConfiguration(), clientTLS))
	if err != nil {
		return nil, err
	}
	session, err := node.Connect(address)
	if err != nil {
		_ = node.Close()
		return nil, err
	}
	channel, bridgeDone, err := openRoot[T](session, configuration.Capacity)
	if err != nil {
		_ = node.Close()
		return nil, err
	}
	go func() {
		<-bridgeDone
		_ = node.Close()
	}()
	return channel, nil
}

func normalizeConfig(configurations []Config) (Config, error) {
	if len(configurations) > 1 {
		return Config{}, errors.New("netchan: at most one configuration is accepted")
	}
	configuration := Config{}
	if len(configurations) == 1 {
		configuration = configurations[0]
	}
	if configuration.Capacity < 0 {
		return Config{}, errors.New("netchan: channel capacity cannot be negative")
	}
	if configuration.TLS == nil {
		return Config{}, errors.New("netchan: TLS configuration is required")
	}
	return configuration, nil
}

func roleTLSConfiguration(configuration *tls.Config) (*tls.Config, error) {
	cloned := configuration.Clone()
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS13
	}
	return cloned, nil
}

func unusedTLSConfiguration() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13}
}

func forwardListenerLifecycle[T any](node *node, accepted chan *Channel[T], reported chan error, done chan struct{}) {
	defer close(accepted)
	defer close(reported)
	defer close(done)
	for {
		select {
		case err := <-node.errors:
			select {
			case reported <- err:
			case <-node.done:
				return
			}
		case <-node.done:
			return
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// END: Public native channel facade
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Node, listener, and session discovery
////////////////////////////////////////////////////////////////////////////////

type nodeOption func(*nodeConfiguration) error

type nodeConfiguration struct {
	serverTLS *tls.Config
	clientTLS *tls.Config
}

func withTLSConfigs(server, client *tls.Config) nodeOption {
	return func(configuration *nodeConfiguration) error {
		if server == nil || client == nil {
			return errors.New("netchan: both server and client TLS configurations are required")
		}
		configuration.serverTLS = server.Clone()
		configuration.clientTLS = client.Clone()
		return nil
	}
}

// node is a local network-channel hypervisor.
type node struct {
	configuration  nodeConfiguration
	commands       chan any
	unhandled      chan sessionUnhandledFrame
	errors         chan error
	stopping       chan struct{}
	done           chan struct{}
	handshakeSlots chan struct{}
	reconnectSlots chan struct{}
}

type nodeRegisterSessionCommand struct {
	session *session
	reply   chan error
}

type nodeFindSessionCommand struct {
	identifier string
	create     bool
	reply      chan *session
}

type nodeForgetSessionCommand struct {
	identifier string
	session    *session
}

type nodePublishCommand struct {
	name      string
	published publishedChannel
	reply     chan error
}

type nodeCloseCommand struct {
	reply chan struct{}
}

type nodeRegisterListenerCommand struct {
	listener *networkListener
	reply    chan error
}

type sessionUnhandledFrame struct {
	session *session
	frame   networkFrame
	reply   chan sessionUnhandledResult
}

type sessionUnhandledResult struct {
	incoming chan networkFrame
	err      error
}

// networkListener owns one TCP/TLS accept loop.
type networkListener struct {
	listener net.Listener
	done     chan struct{}
}

// Addr returns the bound listener address. It is useful when listening on port zero.
func (listener *networkListener) Addr() net.Addr {
	return listener.listener.Addr()
}

// Close stops accepting new physical connections.
func (listener *networkListener) Close() error {
	return listener.listener.Close()
}

// Done closes after the accept loop exits.
func (listener *networkListener) Done() <-chan struct{} {
	return listener.done
}

func newNode(options ...nodeOption) (*node, error) {
	configuration := nodeConfiguration{}
	for _, option := range options {
		if err := option(&configuration); err != nil {
			return nil, err
		}
	}
	if configuration.serverTLS == nil || configuration.clientTLS == nil {
		return nil, errors.New("netchan: TLS configuration is required")
	}

	node := &node{
		configuration:  configuration,
		commands:       make(chan any),
		unhandled:      make(chan sessionUnhandledFrame),
		errors:         make(chan error, 32),
		stopping:       make(chan struct{}),
		done:           make(chan struct{}),
		handshakeSlots: make(chan struct{}, maximumConcurrentHandshakes),
		reconnectSlots: make(chan struct{}, maximumNodeSessions),
	}
	go node.run()
	return node, nil
}

// Errors reports listener, handshake, and channel-open failures.
func (node *node) Errors() <-chan error {
	return node.errors
}

// Done closes when the node has stopped.
func (node *node) Done() <-chan struct{} {
	return node.done
}

// Listen starts accepting TLS connections on address.
func (node *node) listen(address string) (*networkListener, error) {
	socket, err := tls.Listen("tcp", address, node.configuration.serverTLS.Clone())
	if err != nil {
		return nil, err
	}
	listener := &networkListener{listener: socket, done: make(chan struct{})}
	reply := make(chan error, 1)
	select {
	case node.commands <- nodeRegisterListenerCommand{listener: listener, reply: reply}:
	case <-node.stopping:
		_ = socket.Close()
		return nil, errors.New("netchan: node closed")
	case <-node.done:
		_ = socket.Close()
		return nil, errors.New("netchan: node closed")
	}
	if err := <-reply; err != nil {
		_ = socket.Close()
		return nil, err
	}
	go node.accept(listener)
	return listener, nil
}

// Connect establishes a logical session and automatically reconnects it after failures.
func (node *node) Connect(address string) (*session, error) {
	identifier, err := randomIdentifier()
	if err != nil {
		return nil, err
	}
	session := newSession(identifier, node.unhandled, disconnectedLease, node.stopping, 0)
	if err := node.registerSession(session); err != nil {
		_ = session.Close()
		return nil, err
	}

	connection, err := node.dialAndHandshake(address, identifier, false)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	attached := session.attach(connection)
	if attached.err != nil {
		_ = connection.Close()
		_ = session.Close()
		return nil, attached.err
	}
	if !node.startReconnect(session, address, attached.disconnected) {
		_ = session.Close()
		return nil, errors.New("netchan: node closed")
	}
	return session, nil
}

// Close terminates sessions owned by the node.
func (node *node) Close() error {
	reply := make(chan struct{})
	select {
	case node.commands <- nodeCloseCommand{reply: reply}:
	case <-node.stopping:
		<-node.done
		return nil
	case <-node.done:
		return nil
	}
	<-reply
	return nil
}

func (node *node) registerSession(session *session) error {
	reply := make(chan error, 1)
	select {
	case node.commands <- nodeRegisterSessionCommand{session: session, reply: reply}:
	case <-node.stopping:
		return errors.New("netchan: node closed")
	case <-node.done:
		return errors.New("netchan: node closed")
	}
	return <-reply
}

func (node *node) accept(listener *networkListener) {
	defer close(listener.done)
	for {
		connection, err := listener.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				node.publishError(err)
			}
			return
		}
		select {
		case node.handshakeSlots <- struct{}{}:
		case <-node.stopping:
			_ = connection.Close()
			return
		case <-node.done:
			_ = connection.Close()
			return
		}
		go func(connection net.Conn) {
			defer func() { <-node.handshakeSlots }()
			handshakeDone := make(chan struct{})
			watcherDone := make(chan struct{})
			go func() {
				defer close(watcherDone)
				select {
				case <-node.stopping:
					_ = connection.Close()
				case <-handshakeDone:
				}
			}()
			node.acceptConnection(connection)
			close(handshakeDone)
			<-watcherDone
		}(connection)
	}
}

func (node *node) acceptConnection(connection net.Conn) {
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		_ = connection.Close()
		node.publishError(err)
		return
	}
	decoder := newHandshakeFrameDecoder(connection)
	hello, err := decoder.decode()
	if err == nil {
		if hello.Kind != frameHello && hello.Kind != frameHelloResume {
			err = fmt.Errorf("%w: unexpected handshake frame %d", ErrMalformedFrame, hello.Kind)
		} else {
			err = validateHandshake(hello, hello.Kind)
		}
	}
	if err != nil {
		_ = connection.Close()
		node.publishError(err)
		return
	}
	reply := make(chan *session, 1)
	select {
	case node.commands <- nodeFindSessionCommand{identifier: hello.SessionID, create: hello.Kind == frameHello, reply: reply}:
	case <-node.stopping:
		_ = connection.Close()
		return
	case <-node.done:
		_ = connection.Close()
		return
	}
	session := <-reply
	if session == nil {
		responseKind := frameSessionUnknown
		if hello.Kind == frameHello {
			responseKind = frameSessionRejected
		}
		response := networkFrame{Version: protocolVersion, Kind: responseKind, SessionID: hello.SessionID}
		_ = newFrameEncoder(connection).encode(response)
		_ = connection.Close()
		if hello.Kind == frameHello {
			node.publishError(ErrSessionRejected)
		}
		return
	}
	accepted := networkFrame{Version: protocolVersion, Kind: frameHelloAccepted, SessionID: hello.SessionID}
	if err := newFrameEncoder(connection).encode(accepted); err != nil {
		_ = connection.Close()
		node.publishError(err)
		return
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		node.publishError(err)
		return
	}
	attached := session.attach(connection)
	if attached.err != nil {
		_ = connection.Close()
		node.publishError(attached.err)
	}
}

func (node *node) dialAndHandshake(address, sessionID string, resume bool) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	configuration := node.configuration.clientTLS.Clone()
	// Context is confined to the net.Dialer boundary. The session and application
	// still express cancellation exclusively through owner channels.
	dialContext, cancelDial := context.WithCancel(context.Background())
	dialDone := make(chan struct{})
	dialWatcherDone := make(chan struct{})
	go func() {
		defer close(dialWatcherDone)
		select {
		case <-node.stopping:
			cancelDial()
		case <-dialDone:
		}
	}()
	rawConnection, err := dialer.DialContext(dialContext, "tcp", address)
	close(dialDone)
	<-dialWatcherDone
	cancelDial()
	if err != nil {
		return nil, err
	}
	connection := tls.Client(rawConnection, configuration)
	handshakeDone := make(chan struct{})
	watcherDone := make(chan struct{})
	defer func() {
		close(handshakeDone)
		<-watcherDone
	}()
	go func() {
		defer close(watcherDone)
		select {
		case <-node.stopping:
			_ = connection.Close()
		case <-handshakeDone:
		}
	}()
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	helloKind := frameHello
	if resume {
		helloKind = frameHelloResume
	}
	hello := networkFrame{Version: protocolVersion, Kind: helloKind, SessionID: sessionID}
	if err := newFrameEncoder(connection).encode(hello); err != nil {
		_ = connection.Close()
		return nil, err
	}
	accepted, err := newHandshakeFrameDecoder(connection).decode()
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := validateHandshakeResponse(accepted, sessionID, resume); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func validateHandshakeResponse(response networkFrame, sessionID string, resume bool) error {
	if response.SessionID != sessionID {
		return fmt.Errorf("%w: handshake belongs to another session", ErrMalformedFrame)
	}
	switch response.Kind {
	case frameHelloAccepted:
		return validateHandshake(response, frameHelloAccepted)
	case frameSessionUnknown:
		if err := validateHandshake(response, frameSessionUnknown); err != nil {
			return err
		}
		if !resume {
			return fmt.Errorf("%w: a fresh session received an unknown-session response", ErrMalformedFrame)
		}
		return ErrSessionExpired
	case frameSessionRejected:
		if err := validateHandshake(response, frameSessionRejected); err != nil {
			return err
		}
		if resume {
			return fmt.Errorf("%w: a resumed session received a fresh-session rejection", ErrMalformedFrame)
		}
		return ErrSessionRejected
	default:
		return validateHandshake(response, frameHelloAccepted)
	}
}

func (node *node) startReconnect(session *session, address string, disconnected <-chan struct{}) bool {
	select {
	case <-node.stopping:
		return false
	case <-node.done:
		return false
	default:
	}
	select {
	case node.reconnectSlots <- struct{}{}:
	case <-node.stopping:
		return false
	case <-node.done:
		return false
	}
	select {
	case <-node.stopping:
		<-node.reconnectSlots
		return false
	case <-node.done:
		<-node.reconnectSlots
		return false
	default:
	}
	go func() {
		defer func() { <-node.reconnectSlots }()
		node.reconnect(session, address, disconnected)
	}()
	return true
}

func (node *node) reconnect(session *session, address string, disconnected <-chan struct{}) {
	for {
		select {
		case <-disconnected:
		case <-session.done:
			return
		case <-node.done:
			return
		case <-node.stopping:
			return
		}

		delay := defaultReconnectDelay
		for {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-session.done:
				timer.Stop()
				return
			case <-node.done:
				timer.Stop()
				return
			case <-node.stopping:
				timer.Stop()
				return
			}

			connection, err := node.dialAndHandshake(address, session.id, true)
			if err != nil {
				session.publishError(err)
				if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrMalformedFrame) {
					_ = session.Close()
					return
				}
				if delay < maximumReconnectDelay {
					delay *= 2
					if delay > maximumReconnectDelay {
						delay = maximumReconnectDelay
					}
				}
				continue
			}
			attached := session.attach(connection)
			if attached.err != nil {
				_ = connection.Close()
				return
			}
			disconnected = attached.disconnected
			break
		}
	}
}

func (node *node) run() {
	sessions := make(map[string]*session)
	channels := make(map[string]publishedChannel)
	listeners := make(map[*networkListener]struct{})
	var closeReply chan struct{}
	defer func() {
		for listener := range listeners {
			_ = listener.Close()
		}
		for listener := range listeners {
			<-listener.done
		}
		// Filling every slot is a channel-only barrier: active handshake workers
		// cannot appear after the accept loops stop and must release their slots
		// before node shutdown can finish.
		for slot := 0; slot < cap(node.handshakeSlots); slot++ {
			node.handshakeSlots <- struct{}{}
		}
		for slot := 0; slot < cap(node.handshakeSlots); slot++ {
			<-node.handshakeSlots
		}
		for _, session := range sessions {
			<-session.done
		}
		// Reconnect workers own a slot before shutdown begins. Filling the channel
		// waits for every worker without introducing a shared wait counter.
		for slot := 0; slot < cap(node.reconnectSlots); slot++ {
			node.reconnectSlots <- struct{}{}
		}
		for slot := 0; slot < cap(node.reconnectSlots); slot++ {
			<-node.reconnectSlots
		}
		close(node.done)
		if closeReply != nil {
			close(closeReply)
		}
	}()

	for {
		select {
		case rawCommand := <-node.commands:
			switch command := rawCommand.(type) {
			case nodeRegisterSessionCommand:
				if _, exists := sessions[command.session.id]; exists {
					command.reply <- errors.New("netchan: session already registered")
					continue
				}
				sessions[command.session.id] = command.session
				go forgetCompletedSession(node, command.session)
				command.reply <- nil

			case nodeFindSessionCommand:
				session := sessions[command.identifier]
				if session != nil {
					select {
					case <-session.done:
						delete(sessions, command.identifier)
						session = nil
					default:
					}
				}
				if command.create && session != nil {
					command.reply <- nil
					continue
				}
				if session == nil && command.create {
					for identifier, candidate := range sessions {
						select {
						case <-candidate.done:
							delete(sessions, identifier)
						default:
						}
					}
					if len(sessions) >= maximumNodeSessions {
						command.reply <- nil
						continue
					}
					session = newSession(command.identifier, node.unhandled, disconnectedLease, node.stopping, rootOpenTimeout)
					sessions[command.identifier] = session
					go forgetCompletedSession(node, session)
				}
				command.reply <- session

			case nodeForgetSessionCommand:
				if sessions[command.identifier] == command.session {
					delete(sessions, command.identifier)
				}

			case nodePublishCommand:
				if _, exists := channels[command.name]; exists {
					command.reply <- fmt.Errorf("netchan: channel %q is already published", command.name)
					continue
				}
				channels[command.name] = command.published
				command.reply <- nil

			case nodeRegisterListenerCommand:
				listeners[command.listener] = struct{}{}
				command.reply <- nil

			case nodeCloseCommand:
				closeReply = command.reply
				close(node.stopping)
				return
			}

		case unhandled := <-node.unhandled:
			published, exists := channels[unhandled.frame.Name]
			if !exists || published.schema != unhandled.frame.Schema || published.direction != unhandled.frame.Direction {
				node.publishError(fmt.Errorf("%w: channel %q is unavailable for schema %x", ErrSchemaMismatch, unhandled.frame.Name, unhandled.frame.Schema))
				unhandled.reply <- sessionUnhandledResult{err: fmt.Errorf("%w: channel %q is unavailable", ErrSchemaMismatch, unhandled.frame.Name)}
				continue
			}
			incoming := make(chan networkFrame, channelFrameBufferSize)
			published.start(unhandled.session, unhandled.frame.ChannelID, incoming)
			unhandled.reply <- sessionUnhandledResult{incoming: incoming}
		}
	}
}

func forgetCompletedSession(node *node, session *session) {
	select {
	case <-session.done:
	case <-node.done:
		return
	}
	select {
	case node.commands <- nodeForgetSessionCommand{identifier: session.id, session: session}:
	case <-node.stopping:
	case <-node.done:
	}
}

func (node *node) publishError(err error) {
	select {
	case <-node.done:
		return
	default:
	}
	select {
	case node.errors <- err:
	case <-node.done:
	default:
	}
}

////////////////////////////////////////////////////////////////////////////////
// END: Node, listener, and session discovery
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Logical session actor and reconnection
////////////////////////////////////////////////////////////////////////////////

// sessionEvent reports changes in the physical connection backing a logical session.
type sessionEvent struct {
	Connected bool
	Err       error
}

// session owns one logical peer relationship. Physical connections may be replaced
// without replacing the channels carried by the session.
type session struct {
	id                   string
	commands             chan any
	sends                chan sessionSendCommand
	confirmedSends       chan sessionSendCommand
	dataSends            chan sessionSendCommand
	events               chan sessionEvent
	errors               chan error
	done                 chan struct{}
	idleLease            time.Duration
	ownerDone            <-chan struct{}
	rootAdmissionTimeout time.Duration
	decodeSlots          chan struct{}
	encodeSlots          chan struct{}
}

type sessionSendCommand struct {
	frame                networkFrame
	reply                chan error
	reservedPayloadBytes uint64
}

type sessionRegisterChannelCommand struct {
	channelID string
	incoming  chan networkFrame
	reply     chan error
}

type sessionUnregisterChannelCommand struct {
	channelID string
}

type sessionAttachCommand struct {
	connection net.Conn
	reply      chan sessionAttachResult
}

type sessionAttachResult struct {
	disconnected <-chan struct{}
	err          error
}

type sessionCloseCommand struct {
	reply chan struct{}
}

type sessionDisconnectCommand struct {
	reply chan struct{}
}

type sessionAbortCommand struct {
	reply chan struct{}
}

type sessionReserveExportCapabilityCommand struct {
	identity   channelCapabilityIdentity
	channel    reflect.Value
	proposedID string
	reply      chan sessionReserveExportCapabilityResult
}

type sessionReserveExportCapabilityResult struct {
	reservation capabilityReservation
	incoming    chan networkFrame
	err         error
}

type sessionCommitExportCapabilityCommand struct {
	token uint64
	reply chan bool
}

type sessionCancelExportCapabilityCommand struct {
	token uint64
}

type sessionBeginExportCapabilityCloseCommand struct {
	identifier string
	reply      chan error
}

type sessionReserveImportCapabilityCommand struct {
	identifier  string
	channelType reflect.Type
	capacity    int
	reply       chan sessionReserveImportCapabilityResult
}

type sessionReserveImportCapabilityResult struct {
	reservation capabilityReservation
	channel     reflect.Value
	incoming    chan networkFrame
	err         error
}

type sessionCommitImportCapabilityCommand struct {
	token uint64
	reply chan bool
}

type sessionCancelImportCapabilityCommand struct {
	token uint64
}

type sessionReserveDecodedBytesCommand struct {
	bytes uint64
	reply chan error
}

type sessionReleaseDecodedBytesCommand struct {
	bytes uint64
}

type sessionReservePreparedBytesCommand struct {
	bytes uint64
	reply chan error
}

type sessionReleasePreparedBytesCommand struct {
	bytes uint64
}

type capabilityReservation struct {
	identifier string
	token      uint64
}

type exportedChannelCapability struct {
	identity     channelCapabilityIdentity
	channel      reflect.Value
	incoming     chan networkFrame
	reservations int
	active       bool
	terminal     bool
	routingBytes uint64
	forgetQueued bool
}

type importedChannelCapability struct {
	channel      reflect.Value
	channelType  reflect.Type
	capacity     int
	incoming     chan networkFrame
	reservations int
	active       bool
	terminal     bool
	allocation   uint64
	routingBytes uint64
	forgetSeen   bool
}

type applicationTransferKey struct {
	channelID  string
	transferID string
}

type channelCapabilityIdentity struct {
	pointer   uintptr
	direction channelDirection
	schema    schemaHash
}

func newSession(identifier string, unhandled chan<- sessionUnhandledFrame, idleLease time.Duration, ownerDone <-chan struct{}, rootAdmissionTimeout time.Duration) *session {
	session := &session{
		id:                   identifier,
		commands:             make(chan any),
		sends:                make(chan sessionSendCommand),
		confirmedSends:       make(chan sessionSendCommand),
		dataSends:            make(chan sessionSendCommand),
		events:               make(chan sessionEvent, 16),
		errors:               make(chan error, 16),
		done:                 make(chan struct{}),
		idleLease:            idleLease,
		ownerDone:            ownerDone,
		rootAdmissionTimeout: rootAdmissionTimeout,
		decodeSlots:          make(chan struct{}, 1),
		encodeSlots:          make(chan struct{}, maximumConcurrentEncodings),
	}
	session.decodeSlots <- struct{}{}
	for slot := 0; slot < maximumConcurrentEncodings; slot++ {
		session.encodeSlots <- struct{}{}
	}
	go session.run(unhandled)
	return session
}

// ID returns the stable identifier retained across reconnects.
func (session *session) ID() string {
	return session.id
}

// Done closes after the logical session has been terminated.
func (session *session) Done() <-chan struct{} {
	return session.done
}

// Errors reports transport and protocol errors without mixing them with channel values.
func (session *session) Errors() <-chan error {
	return session.errors
}

// Events reports connection loss and recovery for observability.
func (session *session) Events() <-chan sessionEvent {
	return session.events
}

// Close terminates the logical session and all future reconnect attempts.
func (session *session) Close() error {
	reply := make(chan struct{})
	select {
	case session.commands <- sessionCloseCommand{reply: reply}:
	case <-session.done:
		return nil
	}
	<-reply
	return nil
}

func (session *session) abort() error {
	reply := make(chan struct{})
	select {
	case session.commands <- sessionAbortCommand{reply: reply}:
	case <-session.done:
		return nil
	}
	<-reply
	return nil
}

func (session *session) send(frame networkFrame) error {
	reply := make(chan error, 1)
	select {
	case session.sends <- sessionSendCommand{frame: frame, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	return <-reply
}

// sendConfirmed retains a control frame until the peer's transport actor has
// acknowledged it. Channel closure uses this boundary so a following session
// shutdown cannot overtake the final channel frame on the wire.
func (session *session) sendConfirmed(frame networkFrame) error {
	reply := make(chan error, 1)
	select {
	case session.confirmedSends <- sessionSendCommand{frame: frame, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	return <-reply
}

// sendData transfers ownership of one application value to the session actor.
// The actor retains the frame across transport acknowledgements until the peer
// confirms that its application received the value.
func (session *session) sendData(frame networkFrame) error {
	reservedBytes := uint64(len(frame.Payload))
	if err := session.reservePreparedBytes(reservedBytes); err != nil {
		return err
	}
	reply := make(chan error, 1)
	select {
	case session.dataSends <- sessionSendCommand{frame: frame, reply: reply, reservedPayloadBytes: reservedBytes}:
	case <-session.done:
		session.releasePreparedBytes(reservedBytes)
		return errSessionClosed
	}
	err := <-reply
	if err != nil {
		session.releasePreparedBytes(reservedBytes)
	}
	return err
}

func (session *session) registerChannel(channelID string, incoming chan networkFrame) error {
	reply := make(chan error, 1)
	select {
	case session.commands <- sessionRegisterChannelCommand{channelID: channelID, incoming: incoming, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	return <-reply
}

func (session *session) unregisterChannel(channelID string) {
	select {
	case session.commands <- sessionUnregisterChannelCommand{channelID: channelID}:
	case <-session.done:
	}
}

func (session *session) attach(connection net.Conn) sessionAttachResult {
	reply := make(chan sessionAttachResult, 1)
	select {
	case session.commands <- sessionAttachCommand{connection: connection, reply: reply}:
	case <-session.done:
		return sessionAttachResult{err: errSessionClosed}
	}
	return <-reply
}

func (session *session) reserveExportCapability(identity channelCapabilityIdentity, channel reflect.Value, proposedID string) (capabilityReservation, chan networkFrame, error) {
	reply := make(chan sessionReserveExportCapabilityResult, 1)
	select {
	case session.commands <- sessionReserveExportCapabilityCommand{identity: identity, channel: channel, proposedID: proposedID, reply: reply}:
	case <-session.done:
		return capabilityReservation{}, nil, errSessionClosed
	}
	result := <-reply
	return result.reservation, result.incoming, result.err
}

func (session *session) commitExportCapability(reservation capabilityReservation) bool {
	reply := make(chan bool, 1)
	select {
	case session.commands <- sessionCommitExportCapabilityCommand{token: reservation.token, reply: reply}:
	case <-session.done:
		return false
	}
	return <-reply
}

func (session *session) cancelExportCapability(reservation capabilityReservation) {
	select {
	case session.commands <- sessionCancelExportCapabilityCommand{token: reservation.token}:
	case <-session.done:
	}
}

func (session *session) beginExportCapabilityClose(identifier string) error {
	reply := make(chan error, 1)
	select {
	case session.commands <- sessionBeginExportCapabilityCloseCommand{identifier: identifier, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	select {
	case err := <-reply:
		return err
	case <-session.done:
		return errSessionClosed
	}
}

func (session *session) reserveImportCapability(identifier string, channelType reflect.Type, capacity int) (capabilityReservation, reflect.Value, chan networkFrame, error) {
	reply := make(chan sessionReserveImportCapabilityResult, 1)
	select {
	case session.commands <- sessionReserveImportCapabilityCommand{identifier: identifier, channelType: channelType, capacity: capacity, reply: reply}:
	case <-session.done:
		return capabilityReservation{}, reflect.Value{}, nil, errSessionClosed
	}
	result := <-reply
	return result.reservation, result.channel, result.incoming, result.err
}

func (session *session) commitImportCapability(reservation capabilityReservation) bool {
	reply := make(chan bool, 1)
	select {
	case session.commands <- sessionCommitImportCapabilityCommand{token: reservation.token, reply: reply}:
	case <-session.done:
		return false
	}
	return <-reply
}

func (session *session) cancelImportCapability(reservation capabilityReservation) {
	select {
	case session.commands <- sessionCancelImportCapabilityCommand{token: reservation.token}:
	case <-session.done:
	}
}

func (session *session) releaseDecodeSlot() {
	select {
	case session.decodeSlots <- struct{}{}:
	case <-session.done:
	}
}

func (session *session) reserveDecodedBytes(size uint64) error {
	reply := make(chan error, 1)
	select {
	case session.commands <- sessionReserveDecodedBytesCommand{bytes: size, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	return <-reply
}

func (session *session) releaseDecodedBytes(size uint64) {
	select {
	case session.commands <- sessionReleaseDecodedBytesCommand{bytes: size}:
	case <-session.done:
	}
}

func (session *session) acquireEncodeSlot() bool {
	select {
	case <-session.encodeSlots:
		return true
	case <-session.done:
		return false
	}
}

func (session *session) releaseEncodeSlot() {
	select {
	case session.encodeSlots <- struct{}{}:
	case <-session.done:
	}
}

func (session *session) reservePreparedBytes(size uint64) error {
	reply := make(chan error, 1)
	select {
	case session.commands <- sessionReservePreparedBytesCommand{bytes: size, reply: reply}:
	case <-session.done:
		return errSessionClosed
	}
	return <-reply
}

func (session *session) releasePreparedBytes(size uint64) {
	select {
	case session.commands <- sessionReleasePreparedBytesCommand{bytes: size}:
	case <-session.done:
	}
}

func (session *session) run(unhandled chan<- sessionUnhandledFrame) {
	// An unbuffered ingress keeps at most one decoded wire frame outside the
	// actor's byte accounting and applies TCP backpressure before another frame
	// can allocate its payload.
	connectionEvents := make(chan physicalConnectionEvent)
	channels := make(map[string]chan networkFrame)
	exportedCapabilities := make(map[channelCapabilityIdentity]string)
	exportedCapabilityStates := make(map[string]exportedChannelCapability)
	importedCapabilities := make(map[string]importedChannelCapability)
	exportReservations := make(map[uint64]string)
	importReservations := make(map[uint64]string)
	pending := make([]networkFrame, 0, 16)
	wireQueue := make([]networkFrame, 0, 16)
	applicationPending := make(map[string]networkFrame)
	settledApplication := make(map[string]struct{})
	applicationOrder := make([]string, 0, 16)
	inboundApplication := make(map[applicationTransferKey]uint64)
	transportWaiters := make(map[uint64]chan error)
	preparedByteWaiters := make([]sessionReservePreparedBytesCommand, 0, maximumConcurrentEncodings)
	decodedByteWaiters := make([]sessionReserveDecodedBytesCommand, 0, 1)
	capabilityForgetQueue := make([]string, 0, maximumSessionCapabilities)
	var applicationPendingBytes uint64
	var preparedOutboundBytes uint64
	var inboundApplicationBytes uint64
	var importedCapabilityBytes uint64
	var capabilityRoutingBytes uint64
	var decodedPendingBytes uint64
	var liveCapabilityCount int
	var capabilityTombstones int
	var physical *physicalConnection
	var physicalDisconnected chan struct{}
	var nextOutgoingSequence uint64 = 1
	var nextIncomingSequence uint64 = 1
	var nextCapabilityReservation uint64 = 1
	var lastOutgoingAcknowledgement uint64
	var rootChannelSeen bool
	var lastActivity time.Time
	var leaseTimer *time.Timer
	var leaseExpired <-chan time.Time
	var rootAdmissionTimer *time.Timer
	var rootAdmissionExpired <-chan time.Time
	if session.rootAdmissionTimeout > 0 {
		rootAdmissionTimer = time.NewTimer(session.rootAdmissionTimeout)
		rootAdmissionExpired = rootAdmissionTimer.C
	}
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	grantPreparedByteWaiters := func() {
		for {
			granted := -1
			for index, waiter := range preparedByteWaiters {
				if waiter.bytes <= maximumSessionPreparedBytes-preparedOutboundBytes {
					granted = index
					break
				}
			}
			if granted < 0 {
				return
			}
			waiter := preparedByteWaiters[granted]
			preparedByteWaiters = append(preparedByteWaiters[:granted], preparedByteWaiters[granted+1:]...)
			preparedOutboundBytes += waiter.bytes
			waiter.reply <- nil
		}
	}
	grantDecodedByteWaiters := func() {
		for len(decodedByteWaiters) > 0 {
			waiter := decodedByteWaiters[0]
			if waiter.bytes > maximumSessionDecodedBytes-decodedPendingBytes {
				return
			}
			decodedByteWaiters = decodedByteWaiters[1:]
			decodedPendingBytes += waiter.bytes
			waiter.reply <- nil
		}
	}
	releaseInboundApplication := func(frame networkFrame) {
		if frame.Kind != frameChannelDelivered {
			return
		}
		key := applicationTransferKey{channelID: frame.ChannelID, transferID: frame.TransferID}
		payloadBytes, exists := inboundApplication[key]
		if !exists {
			return
		}
		delete(inboundApplication, key)
		inboundApplicationBytes -= payloadBytes
	}
	queueActorFrame := func(frame networkFrame) error {
		if len(pending) >= maximumPendingFrames {
			return fmt.Errorf("%w: session response window is full", ErrPayloadLimit)
		}
		frame.Version = protocolVersion
		frame.SessionID = session.id
		frame.Sequence = nextOutgoingSequence
		nextOutgoingSequence++
		pending = append(pending, frame)
		if physical != nil {
			wireQueue = append(wireQueue, frame)
		}
		return nil
	}
	scheduleCapabilityForget := func(identifier string, capability exportedChannelCapability) {
		if capability.forgetQueued || !capability.terminal || capability.active || capability.reservations != 0 {
			return
		}
		capability.forgetQueued = true
		exportedCapabilityStates[identifier] = capability
		capabilityForgetQueue = append(capabilityForgetQueue, identifier)
	}
	removeImportedCapability := func(identifier string, capability importedChannelCapability) {
		delete(channels, identifier)
		for key, payloadBytes := range inboundApplication {
			if key.channelID == identifier {
				delete(inboundApplication, key)
				inboundApplicationBytes -= payloadBytes
			}
		}
		if capability.incoming != nil {
			capabilityRoutingBytes -= capability.routingBytes
			liveCapabilityCount--
		} else if capability.terminal {
			capabilityTombstones--
		}
		importedCapabilityBytes -= capability.allocation
		delete(importedCapabilities, identifier)
	}

	defer func() {
		heartbeatTicker.Stop()
		if leaseTimer != nil {
			leaseTimer.Stop()
		}
		if rootAdmissionTimer != nil {
			rootAdmissionTimer.Stop()
		}
		if physical != nil {
			_ = physical.connection.Close()
		}
		for _, incoming := range channels {
			close(incoming)
		}
		for sequence, waiter := range transportWaiters {
			waiter <- errSessionClosed
			delete(transportWaiters, sequence)
		}
		for _, waiter := range preparedByteWaiters {
			waiter.reply <- errSessionClosed
		}
		for _, waiter := range decodedByteWaiters {
			waiter.reply <- errSessionClosed
		}
		close(session.done)
	}()

	for {
		if len(capabilityForgetQueue) > 0 && len(pending) < maximumPendingFrames {
			identifier := capabilityForgetQueue[0]
			capabilityForgetQueue = capabilityForgetQueue[1:]
			capability, exists := exportedCapabilityStates[identifier]
			if exists && capability.forgetQueued {
				if capability.terminal && !capability.active && capability.reservations == 0 {
					if err := queueActorFrame(networkFrame{Kind: frameChannelForgotten, ChannelID: identifier}); err != nil {
						session.publishError(err)
						return
					}
					delete(exportedCapabilities, capability.identity)
					delete(exportedCapabilityStates, identifier)
					capabilityTombstones--
				} else {
					// A new parent may reserve the terminal identity while the
					// response window is full. Clearing the queued marker lets the
					// final commit or cancellation schedule the barrier again.
					capability.forgetQueued = false
					exportedCapabilityStates[identifier] = capability
				}
			}
			continue
		}
		var physicalOutgoing chan physicalWriteCommand
		var physicalCommand physicalWriteCommand
		if physical != nil && len(wireQueue) > 0 {
			physicalOutgoing = physical.outgoing
			physicalCommand = physicalWriteCommand{frame: wireQueue[0]}
		}
		var sendCommands <-chan sessionSendCommand = session.sends
		var confirmedSendCommands <-chan sessionSendCommand = session.confirmedSends
		if len(pending) >= maximumPendingFrames {
			sendCommands = nil
			confirmedSendCommands = nil
		}
		var dataSendCommands <-chan sessionSendCommand
		if len(pending) < maximumPendingFrames &&
			len(applicationPending) < maximumPendingFrames &&
			applicationPendingBytes <= maximumSessionQueuedBytes-uint64(maximumChannelPayloadSize) {
			dataSendCommands = session.dataSends
		}

		select {
		case physicalOutgoing <- physicalCommand:
			wireQueue = wireQueue[1:]

		case command := <-sendCommands:
			releaseInboundApplication(command.frame)
			command.frame.Version = protocolVersion
			command.frame.SessionID = session.id
			command.frame.Sequence = nextOutgoingSequence
			nextOutgoingSequence++
			pending = append(pending, command.frame)
			if physical != nil {
				wireQueue = append(wireQueue, command.frame)
			}
			command.reply <- nil

		case command := <-confirmedSendCommands:
			command.frame.Version = protocolVersion
			command.frame.SessionID = session.id
			command.frame.Sequence = nextOutgoingSequence
			nextOutgoingSequence++
			transportWaiters[command.frame.Sequence] = command.reply
			pending = append(pending, command.frame)
			if physical != nil {
				wireQueue = append(wireQueue, command.frame)
			}

		case command := <-dataSendCommands:
			if _, exists := applicationPending[command.frame.TransferID]; exists {
				command.reply <- fmt.Errorf("%w: duplicate application transfer identifier", ErrMalformedFrame)
				continue
			}
			payloadBytes := uint64(len(command.frame.Payload))
			if command.reservedPayloadBytes != payloadBytes || preparedOutboundBytes < payloadBytes {
				command.reply <- fmt.Errorf("%w: application payload was not reserved", ErrMalformedFrame)
				continue
			}
			preparedOutboundBytes -= payloadBytes
			grantPreparedByteWaiters()
			command.frame.Version = protocolVersion
			command.frame.SessionID = session.id
			command.frame.Sequence = nextOutgoingSequence
			nextOutgoingSequence++
			applicationPending[command.frame.TransferID] = command.frame
			applicationPendingBytes += payloadBytes
			applicationOrder = append(applicationOrder, command.frame.TransferID)
			pending = append(pending, command.frame)
			if physical != nil {
				wireQueue = append(wireQueue, command.frame)
			}
			command.reply <- nil

		case rawCommand := <-session.commands:
			switch command := rawCommand.(type) {
			case sessionRegisterChannelCommand:
				if _, exists := channels[command.channelID]; exists {
					command.reply <- errors.New("netchan: channel is already registered")
					continue
				}
				channels[command.channelID] = command.incoming
				command.reply <- nil

			case sessionUnregisterChannelCommand:
				delete(channels, command.channelID)
				for transferID, frame := range applicationPending {
					if frame.ChannelID == command.channelID {
						if frameIsPending(pending, transferID) {
							settledApplication[transferID] = struct{}{}
							continue
						}
						applicationPendingBytes -= uint64(len(frame.Payload))
						delete(applicationPending, transferID)
						delete(settledApplication, transferID)
					}
				}
				for key, payloadBytes := range inboundApplication {
					if key.channelID == command.channelID {
						delete(inboundApplication, key)
						inboundApplicationBytes -= payloadBytes
					}
				}
				applicationOrder = compactApplicationOrder(applicationOrder, applicationPending)
				if capability, exists := exportedCapabilityStates[command.channelID]; exists {
					capability.active = false
					if capability.incoming != nil {
						capabilityRoutingBytes -= capability.routingBytes
						liveCapabilityCount--
						capabilityTombstones++
					}
					capability.incoming = nil
					capability.terminal = true
					exportedCapabilityStates[command.channelID] = capability
					scheduleCapabilityForget(command.channelID, capability)
				}
				if capability, exists := importedCapabilities[command.channelID]; exists {
					capability.active = false
					if capability.incoming != nil {
						capabilityRoutingBytes -= capability.routingBytes
						liveCapabilityCount--
						capabilityTombstones++
					}
					capability.incoming = nil
					capability.terminal = true
					if capability.forgetSeen && capability.reservations == 0 {
						removeImportedCapability(command.channelID, capability)
					} else {
						importedCapabilities[command.channelID] = capability
					}
				}

			case sessionReserveExportCapabilityCommand:
				if command.proposedID == "" {
					command.reply <- sessionReserveExportCapabilityResult{err: fmt.Errorf("%w: channel capability has no identifier", ErrMalformedFrame)}
					continue
				}
				if identifier := exportedCapabilities[command.identity]; identifier != "" {
					capability := exportedCapabilityStates[identifier]
					token := nextCapabilityReservation
					nextCapabilityReservation++
					capability.reservations++
					exportedCapabilityStates[identifier] = capability
					exportReservations[token] = identifier
					command.reply <- sessionReserveExportCapabilityResult{
						reservation: capabilityReservation{identifier: identifier, token: token},
						incoming:    capability.incoming,
					}
					continue
				}
				if liveCapabilityCount >= maximumSessionCapabilities || capabilityTombstones >= maximumCapabilityTombstones {
					command.reply <- sessionReserveExportCapabilityResult{err: fmt.Errorf("%w: session capability limit reached", ErrPayloadLimit)}
					continue
				}
				_, exportedIdentifierExists := exportedCapabilityStates[command.proposedID]
				_, importedIdentifierExists := importedCapabilities[command.proposedID]
				if _, exists := channels[command.proposedID]; exists || exportedIdentifierExists || importedIdentifierExists {
					command.reply <- sessionReserveExportCapabilityResult{err: fmt.Errorf("%w: channel capability identifier is already registered", ErrMalformedFrame)}
					continue
				}
				routingBytes := uint64(channelFrameBufferSize) * uint64(reflect.TypeOf(networkFrame{}).Size())
				if routingBytes > maximumCapabilityRouteBytes || capabilityRoutingBytes > maximumCapabilityRouteBytes-routingBytes {
					command.reply <- sessionReserveExportCapabilityResult{err: fmt.Errorf("%w: session capability routing budget reached", ErrPayloadLimit)}
					continue
				}
				incoming := make(chan networkFrame, channelFrameBufferSize)
				token := nextCapabilityReservation
				nextCapabilityReservation++
				exportedCapabilities[command.identity] = command.proposedID
				exportedCapabilityStates[command.proposedID] = exportedChannelCapability{
					identity: command.identity, channel: command.channel, incoming: incoming, reservations: 1, routingBytes: routingBytes,
				}
				capabilityRoutingBytes += routingBytes
				liveCapabilityCount++
				channels[command.proposedID] = incoming
				exportReservations[token] = command.proposedID
				command.reply <- sessionReserveExportCapabilityResult{
					reservation: capabilityReservation{identifier: command.proposedID, token: token},
					incoming:    incoming,
				}

			case sessionCommitExportCapabilityCommand:
				identifier, reserved := exportReservations[command.token]
				if reserved {
					delete(exportReservations, command.token)
				}
				capability, exists := exportedCapabilityStates[identifier]
				if !reserved || !exists {
					command.reply <- false
					continue
				}
				if capability.reservations == 0 {
					command.reply <- false
					session.publishError(fmt.Errorf("%w: export capability reservation underflow", ErrMalformedFrame))
					return
				}
				capability.reservations--
				start := !capability.active && !capability.terminal
				if start {
					capability.active = true
				}
				exportedCapabilityStates[identifier] = capability
				scheduleCapabilityForget(identifier, capability)
				command.reply <- start

			case sessionCancelExportCapabilityCommand:
				identifier, reserved := exportReservations[command.token]
				if reserved {
					delete(exportReservations, command.token)
				}
				capability, exists := exportedCapabilityStates[identifier]
				if !reserved || !exists {
					continue
				}
				if capability.reservations == 0 {
					session.publishError(fmt.Errorf("%w: export capability reservation underflow", ErrMalformedFrame))
					return
				}
				capability.reservations--
				if capability.reservations == 0 && !capability.active && !capability.terminal {
					delete(channels, identifier)
					capabilityRoutingBytes -= capability.routingBytes
					delete(exportedCapabilities, capability.identity)
					delete(exportedCapabilityStates, identifier)
					liveCapabilityCount--
				} else {
					exportedCapabilityStates[identifier] = capability
					scheduleCapabilityForget(identifier, capability)
				}

			case sessionBeginExportCapabilityCloseCommand:
				capability, exists := exportedCapabilityStates[command.identifier]
				if !exists {
					command.reply <- ErrChannelClosed
					continue
				}
				if !capability.terminal {
					capability.terminal = true
				}
				exportedCapabilityStates[command.identifier] = capability
				command.reply <- nil

			case sessionReserveImportCapabilityCommand:
				if command.identifier == "" {
					command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: channel capability has no identifier", ErrMalformedFrame)}
					continue
				}
				if capability, exists := importedCapabilities[command.identifier]; exists {
					if capability.channelType != command.channelType || capability.capacity != command.capacity {
						command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: channel capability changed its type or capacity", ErrMalformedFrame)}
						continue
					}
					if capability.forgetSeen {
						command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: forgotten channel capability was reused", ErrMalformedFrame)}
						continue
					}
					token := nextCapabilityReservation
					nextCapabilityReservation++
					capability.reservations++
					importedCapabilities[command.identifier] = capability
					importReservations[token] = command.identifier
					command.reply <- sessionReserveImportCapabilityResult{
						reservation: capabilityReservation{identifier: command.identifier, token: token},
						channel:     capability.channel,
						incoming:    capability.incoming,
					}
					continue
				}
				_, exportedIdentifierExists := exportedCapabilityStates[command.identifier]
				if _, exists := channels[command.identifier]; exists || exportedIdentifierExists {
					command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: channel capability identifier is already registered", ErrMalformedFrame)}
					continue
				}
				if liveCapabilityCount >= maximumSessionCapabilities || capabilityTombstones >= maximumCapabilityTombstones {
					command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: session capability limit reached", ErrPayloadLimit)}
					continue
				}
				internalType := reflect.ChanOf(reflect.BothDir, command.channelType.Elem())
				allocation := uint64(command.capacity) * uint64(command.channelType.Elem().Size()) // #nosec G115 -- command capacities are validated before admission.
				if allocation > maximumCapabilityBytes || importedCapabilityBytes > maximumCapabilityBytes-allocation {
					command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: session capability allocation budget reached", ErrPayloadLimit)}
					continue
				}
				routingBytes := uint64(channelFrameBufferSize) * uint64(reflect.TypeOf(networkFrame{}).Size())
				if routingBytes > maximumCapabilityRouteBytes || capabilityRoutingBytes > maximumCapabilityRouteBytes-routingBytes {
					command.reply <- sessionReserveImportCapabilityResult{err: fmt.Errorf("%w: session capability routing budget reached", ErrPayloadLimit)}
					continue
				}
				channel := reflect.MakeChan(internalType, command.capacity)
				incoming := make(chan networkFrame, channelFrameBufferSize)
				token := nextCapabilityReservation
				nextCapabilityReservation++
				importedCapabilities[command.identifier] = importedChannelCapability{
					channel: channel, channelType: command.channelType, capacity: command.capacity,
					incoming: incoming, reservations: 1, allocation: allocation, routingBytes: routingBytes,
				}
				liveCapabilityCount++
				importedCapabilityBytes += allocation
				capabilityRoutingBytes += routingBytes
				channels[command.identifier] = incoming
				importReservations[token] = command.identifier
				command.reply <- sessionReserveImportCapabilityResult{
					reservation: capabilityReservation{identifier: command.identifier, token: token},
					channel:     channel,
					incoming:    incoming,
				}

			case sessionCommitImportCapabilityCommand:
				identifier, reserved := importReservations[command.token]
				if reserved {
					delete(importReservations, command.token)
				}
				capability, exists := importedCapabilities[identifier]
				if !reserved || !exists {
					command.reply <- false
					continue
				}
				if capability.reservations == 0 {
					command.reply <- false
					session.publishError(fmt.Errorf("%w: import capability reservation underflow", ErrMalformedFrame))
					return
				}
				capability.reservations--
				start := !capability.active && !capability.terminal
				if start {
					capability.active = true
				}
				if capability.forgetSeen && !capability.active && capability.reservations == 0 {
					removeImportedCapability(identifier, capability)
				} else {
					importedCapabilities[identifier] = capability
				}
				command.reply <- start

			case sessionCancelImportCapabilityCommand:
				identifier, reserved := importReservations[command.token]
				if reserved {
					delete(importReservations, command.token)
				}
				capability, exists := importedCapabilities[identifier]
				if !reserved || !exists {
					continue
				}
				if capability.reservations == 0 {
					session.publishError(fmt.Errorf("%w: import capability reservation underflow", ErrMalformedFrame))
					return
				}
				capability.reservations--
				if capability.reservations == 0 && !capability.active && (capability.forgetSeen || !capability.terminal) {
					removeImportedCapability(identifier, capability)
				} else {
					importedCapabilities[identifier] = capability
				}

			case sessionReserveDecodedBytesCommand:
				if command.bytes > maximumSessionDecodedBytes {
					command.reply <- fmt.Errorf("%w: decoded value exceeds session budget", ErrPayloadLimit)
					continue
				}
				if decodedPendingBytes <= maximumSessionDecodedBytes-command.bytes {
					decodedPendingBytes += command.bytes
					command.reply <- nil
					continue
				}
				decodedByteWaiters = append(decodedByteWaiters, command)

			case sessionReleaseDecodedBytesCommand:
				if command.bytes > decodedPendingBytes {
					session.publishError(fmt.Errorf("%w: decoded-value accounting underflow", ErrMalformedFrame))
					decodedPendingBytes = 0
					continue
				}
				decodedPendingBytes -= command.bytes
				grantDecodedByteWaiters()

			case sessionReservePreparedBytesCommand:
				if command.bytes > maximumSessionPreparedBytes {
					command.reply <- fmt.Errorf("%w: prepared payload exceeds session budget", ErrPayloadLimit)
					continue
				}
				if preparedOutboundBytes <= maximumSessionPreparedBytes-command.bytes {
					preparedOutboundBytes += command.bytes
					command.reply <- nil
					continue
				}
				preparedByteWaiters = append(preparedByteWaiters, command)

			case sessionReleasePreparedBytesCommand:
				if command.bytes > preparedOutboundBytes {
					session.publishError(fmt.Errorf("%w: prepared-payload accounting underflow", ErrMalformedFrame))
					preparedOutboundBytes = 0
					continue
				}
				preparedOutboundBytes -= command.bytes
				grantPreparedByteWaiters()

			case sessionAttachCommand:
				if leaseTimer != nil {
					leaseTimer.Stop()
					leaseTimer = nil
					leaseExpired = nil
				}
				if physical != nil {
					_ = physical.connection.Close()
					if physicalDisconnected != nil {
						close(physicalDisconnected)
					}
				}
				physical = runPhysicalConnection(command.connection, connectionEvents, session.done, session.ownerDone)
				lastActivity = time.Now()
				physicalDisconnected = make(chan struct{})
				wireQueue = wireQueue[:0]
				for _, frame := range pending {
					wireQueue = append(wireQueue, frame)
				}
				for _, transferID := range applicationOrder {
					frame, exists := applicationPending[transferID]
					if !exists {
						continue
					}
					if frameIsPending(pending, transferID) {
						continue
					}
					frame.Sequence = nextOutgoingSequence
					nextOutgoingSequence++
					applicationPending[transferID] = frame
					pending = append(pending, frame)
					wireQueue = append(wireQueue, frame)
				}
				session.publishEvent(sessionEvent{Connected: true})
				command.reply <- sessionAttachResult{disconnected: physicalDisconnected}

			case sessionCloseCommand:
				flushSessionClose(physical, wireQueue, session.id)
				close(command.reply)
				return

			case sessionDisconnectCommand:
				if physical != nil {
					_ = physical.connection.Close()
				}
				close(command.reply)

			case sessionAbortCommand:
				if physical != nil {
					_ = physical.connection.Close()
				}
				close(command.reply)
				return
			}

		case event := <-connectionEvents:
			if physical == nil || event.connection != physical {
				continue
			}
			if event.err != nil {
				_ = physical.connection.Close()
				if errors.Is(event.err, ErrMalformedFrame) {
					session.publishError(event.err)
					session.publishEvent(sessionEvent{Err: event.err})
					return
				}
				physical = nil
				wireQueue = wireQueue[:0]
				if session.idleLease > 0 {
					leaseTimer = time.NewTimer(session.idleLease)
					leaseExpired = leaseTimer.C
				}
				if physicalDisconnected != nil {
					close(physicalDisconnected)
					physicalDisconnected = nil
				}
				if !errors.Is(event.err, io.EOF) && !errors.Is(event.err, net.ErrClosed) {
					session.publishError(event.err)
				}
				session.publishEvent(sessionEvent{Err: event.err})
				continue
			}

			frame := event.frame
			lastActivity = time.Now()
			if frame.SessionID != session.id {
				err := fmt.Errorf("%w: frame belongs to another session", ErrMalformedFrame)
				session.publishError(err)
				_ = physical.connection.Close()
				return
			}
			if frame.Kind == frameHeartbeat {
				continue
			}
			switch frame.Kind {
			case frameHello, frameHelloResume, frameHelloAccepted, frameSessionUnknown, frameSessionRejected:
				err := fmt.Errorf("%w: handshake frame is invalid inside an established session", ErrMalformedFrame)
				session.publishError(err)
				_ = physical.connection.Close()
				return
			}
			if frame.Kind == frameAcknowledgement {
				if frame.Ack < lastOutgoingAcknowledgement || frame.Ack >= nextOutgoingSequence {
					err := fmt.Errorf("%w: acknowledgement is outside the sent sequence", ErrMalformedFrame)
					session.publishError(err)
					_ = physical.connection.Close()
					return
				}
				lastOutgoingAcknowledgement = frame.Ack
				removeCount := 0
				for removeCount < len(pending) && pending[removeCount].Sequence <= frame.Ack {
					removeCount++
				}
				for index := 0; index < removeCount; index++ {
					acknowledged := pending[index]
					if acknowledged.Kind != frameChannelValue {
						continue
					}
					if _, settled := settledApplication[acknowledged.TransferID]; !settled {
						continue
					}
					if applicationFrame, exists := applicationPending[acknowledged.TransferID]; exists {
						applicationPendingBytes -= uint64(len(applicationFrame.Payload))
						delete(applicationPending, acknowledged.TransferID)
					}
					delete(settledApplication, acknowledged.TransferID)
				}
				pending = pending[removeCount:]
				for sequence, waiter := range transportWaiters {
					if sequence <= frame.Ack {
						waiter <- nil
						delete(transportWaiters, sequence)
					}
				}
				continue
			}
			if frame.Kind == frameSessionClosed {
				return
			}
			if frame.Sequence < nextIncomingSequence {
				physical.send(acknowledgementFrame(session.id, nextIncomingSequence-1))
				continue
			}
			if frame.Sequence != nextIncomingSequence {
				session.publishError(fmt.Errorf("%w: frame sequence is out of order", ErrMalformedFrame))
				_ = physical.connection.Close()
				return
			}
			if frame.Kind == frameChannelValue {
				key := applicationTransferKey{channelID: frame.ChannelID, transferID: frame.TransferID}
				if _, exists := inboundApplication[key]; !exists {
					payloadBytes := uint64(len(frame.Payload))
					if payloadBytes > maximumSessionQueuedBytes || inboundApplicationBytes > maximumSessionQueuedBytes-payloadBytes {
						err := fmt.Errorf("%w: session receive byte budget reached", ErrPayloadLimit)
						session.publishError(err)
						_ = physical.connection.Close()
						return
					}
					inboundApplication[key] = payloadBytes
					inboundApplicationBytes += payloadBytes
				}
			}

			if frame.Kind == frameChannelDelivered {
				if applicationFrame, exists := applicationPending[frame.TransferID]; exists {
					if applicationFrame.ChannelID != frame.ChannelID {
						session.publishError(fmt.Errorf("%w: delivery acknowledgement belongs to another channel", ErrMalformedFrame))
						_ = physical.connection.Close()
						return
					}
					if frameIsPending(pending, frame.TransferID) {
						settledApplication[frame.TransferID] = struct{}{}
					} else {
						applicationPendingBytes -= uint64(len(applicationFrame.Payload))
						delete(applicationPending, frame.TransferID)
						delete(settledApplication, frame.TransferID)
					}
					if len(applicationOrder) > maximumPendingFrames*2 {
						applicationOrder = compactApplicationOrder(applicationOrder, applicationPending)
					}
				}
			}
			if frame.Kind == frameChannelForgotten {
				if capability, exists := importedCapabilities[frame.ChannelID]; exists {
					capability.forgetSeen = true
					if !capability.active && capability.reservations == 0 {
						removeImportedCapability(frame.ChannelID, capability)
					} else {
						importedCapabilities[frame.ChannelID] = capability
					}
				}
				nextIncomingSequence++
				physical.send(acknowledgementFrame(session.id, nextIncomingSequence-1))
				continue
			}
			if frame.Kind == frameOpenChannel {
				result := sessionUnhandledResult{}
				if rootChannelSeen {
					result.err = fmt.Errorf("%w: a session can open only one root channel", ErrMalformedFrame)
				} else {
					rootChannelSeen = true
					reply := make(chan sessionUnhandledResult, 1)
					select {
					case unhandled <- sessionUnhandledFrame{session: session, frame: frame, reply: reply}:
					case <-session.ownerDone:
						return
					case <-session.done:
						return
					case <-rootAdmissionExpired:
						session.publishError(fmt.Errorf("%w: root channel was not opened before the admission timeout", ErrPayloadLimit))
						return
					}
					select {
					case result = <-reply:
					case <-session.ownerDone:
						return
					case <-session.done:
						return
					case <-rootAdmissionExpired:
						session.publishError(fmt.Errorf("%w: root channel was not opened before the admission timeout", ErrPayloadLimit))
						return
					}
				}
				var response networkFrame
				if result.incoming != nil {
					if rootAdmissionTimer != nil {
						rootAdmissionTimer.Stop()
						rootAdmissionTimer = nil
						rootAdmissionExpired = nil
					}
					channels[frame.ChannelID] = result.incoming
					response = networkFrame{Kind: frameChannelOpened, ChannelID: frame.ChannelID}
				} else {
					response = networkFrame{
						Kind:      frameChannelRevoked,
						ChannelID: frame.ChannelID,
					}
					if result.err != nil {
						response.Error = boundedProtocolError(result.err)
						response.ErrorCode = protocolErrorCodeFor(result.err)
					}
				}
				if err := queueActorFrame(response); err != nil {
					session.publishError(err)
					_ = physical.connection.Close()
					return
				}
			} else if incoming := channels[frame.ChannelID]; incoming != nil {
				select {
				case incoming <- frame:
				case <-session.ownerDone:
					return
				default:
					err := fmt.Errorf("%w: channel frame queue is full", ErrPayloadLimit)
					session.publishError(err)
					_ = physical.connection.Close()
					return
				}
			} else {
				if frame.Kind == frameChannelValue {
					key := applicationTransferKey{channelID: frame.ChannelID, transferID: frame.TransferID}
					if payloadBytes, exists := inboundApplication[key]; exists {
						delete(inboundApplication, key)
						inboundApplicationBytes -= payloadBytes
					}
					rejection := networkFrame{
						Kind: frameChannelRevoked, ChannelID: frame.ChannelID,
						Error: ErrChannelClosed.Error(), ErrorCode: protocolErrorChannelClosed,
					}
					if err := queueActorFrame(rejection); err != nil {
						session.publishError(err)
						_ = physical.connection.Close()
						return
					}
				}
			}

			nextIncomingSequence++
			physical.send(acknowledgementFrame(session.id, nextIncomingSequence-1))

		case <-heartbeatTicker.C:
			if physical == nil {
				continue
			}
			if time.Since(lastActivity) >= heartbeatTimeout {
				_ = physical.connection.Close()
				continue
			}
			physical.send(networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: session.id})

		case <-leaseExpired:
			return

		case <-rootAdmissionExpired:
			session.publishError(fmt.Errorf("%w: root channel was not opened before the admission timeout", ErrPayloadLimit))
			return

		case <-session.ownerDone:
			flushSessionClose(physical, wireQueue, session.id)
			return
		}
	}
}

func acknowledgementFrame(sessionID string, sequence uint64) networkFrame {
	return networkFrame{
		Version:   protocolVersion,
		Kind:      frameAcknowledgement,
		SessionID: sessionID,
		Ack:       sequence,
	}
}

func frameIsPending(frames []networkFrame, transferID string) bool {
	for _, frame := range frames {
		if frame.Kind == frameChannelValue && frame.TransferID == transferID {
			return true
		}
	}
	return false
}

func compactApplicationOrder(order []string, pending map[string]networkFrame) []string {
	compacted := make([]string, 0, len(pending))
	for _, transferID := range order {
		if _, exists := pending[transferID]; exists {
			compacted = append(compacted, transferID)
		}
	}
	return compacted
}

func (session *session) publishError(err error) {
	select {
	case session.errors <- err:
	default:
	}
}

func (session *session) publishEvent(event sessionEvent) {
	select {
	case session.events <- event:
	default:
	}
}

////////////////////////////////////////////////////////////////////////////////
// END: Logical session actor and reconnection
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Native channel bridges and channel capabilities
////////////////////////////////////////////////////////////////////////////////

type publishedChannel struct {
	schema    schemaHash
	direction channelDirection
	start     func(*session, string, chan networkFrame)
}

type outboundTransfer struct {
	reply      chan error
	activation capabilityActivation
	prepared   bool
}

// preparedOutboundFrame separates binary preparation from session admission.
// Capability sources remain dormant after preparation and start only when the
// peer application accepts the containing value.
type preparedOutboundFrame struct {
	frame         networkFrame
	reservedBytes uint64
}

func publishRoot[T any](node *node, accepted chan<- *Channel[T], capacity int) error {
	elementType := reflect.TypeOf((*T)(nil)).Elem()
	if err := validateNetworkType(elementType); err != nil {
		return err
	}
	reply := make(chan error, 1)
	command := nodePublishCommand{
		name: rootChannelName,
		published: publishedChannel{
			schema:    schemaForType(elementType),
			direction: channelBidirectional,
			start: func(session *session, identifier string, incoming chan networkFrame) {
				channel := newChannel[T](capacity)
				channel.abortSession = session.abort
				bridgeDone := startRootChannelBridge(session, identifier, channel, incoming)
				go func() {
					if !offerAcceptedChannel(accepted, channel, session.done, node.done) {
						_ = session.Close()
						return
					}
					<-bridgeDone
					_ = session.Close()
				}()
			},
		},
		reply: reply,
	}
	select {
	case node.commands <- command:
	case <-node.stopping:
		return errors.New("netchan: node closed")
	case <-node.done:
		return errors.New("netchan: node closed")
	}
	select {
	case err := <-reply:
		return err
	case <-node.stopping:
		return errors.New("netchan: node closed")
	case <-node.done:
		return errors.New("netchan: node closed")
	}
}

func offerAcceptedChannel[T any](accepted chan<- *Channel[T], channel *Channel[T], sessionDone, nodeDone <-chan struct{}) (offered bool) {
	defer func() {
		if recover() != nil {
			offered = false
		}
	}()
	select {
	case accepted <- channel:
		return true
	case <-sessionDone:
		return false
	case <-nodeDone:
		return false
	}
}

func openRoot[T any](session *session, capacity int) (*Channel[T], <-chan struct{}, error) {
	identifier, err := randomIdentifier()
	if err != nil {
		return nil, nil, err
	}
	elementType := reflect.TypeOf((*T)(nil)).Elem()
	if err := validateNetworkType(elementType); err != nil {
		return nil, nil, err
	}
	channel := newChannel[T](capacity)
	channel.abortSession = session.abort
	incoming := make(chan networkFrame, channelFrameBufferSize)
	if err := session.registerChannel(identifier, incoming); err != nil {
		return nil, nil, err
	}
	if err := session.send(networkFrame{
		Kind: frameOpenChannel, ChannelID: identifier, Name: rootChannelName,
		Direction: channelBidirectional, Capacity: capacity, Schema: schemaForType(elementType),
	}); err != nil {
		session.unregisterChannel(identifier)
		return nil, nil, err
	}
	openTimer := time.NewTimer(rootOpenTimeout)
	defer openTimer.Stop()
	select {
	case frame, open := <-incoming:
		if !open {
			session.unregisterChannel(identifier)
			return nil, nil, ErrChannelClosed
		}
		switch frame.Kind {
		case frameChannelOpened:
		case frameChannelRevoked:
			session.unregisterChannel(identifier)
			return nil, nil, errorFromNetworkFrame(frame)
		default:
			session.unregisterChannel(identifier)
			return nil, nil, fmt.Errorf("%w: expected channel-open acknowledgement", ErrMalformedFrame)
		}
	case <-session.done:
		return nil, nil, ErrChannelClosed
	case <-openTimer.C:
		session.unregisterChannel(identifier)
		return nil, nil, errors.New("netchan: timed out opening root channel")
	}
	bridgeDone := startRootChannelBridge(session, identifier, channel, incoming)
	return channel, bridgeDone, nil
}

func startRootChannelBridge[T any](session *session, channelID string, channel *Channel[T], incoming chan networkFrame) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRootChannelBridge(session, channelID, channel, incoming)
	}()
	return done
}

func runRootChannelBridge[T any](session *session, channelID string, channel *Channel[T], incoming chan networkFrame) {
	defer session.unregisterChannel(channelID)
	outbound := make(map[string]outboundTransfer)
	outboundFrames := make([]preparedOutboundFrame, 0, 4)
	inboundIdentifiers := make(map[string]struct{})
	delivered := make(map[string]struct{})
	inboundQueue := make([]networkFrame, 0, 4)
	var writeLocal chan<- T
	var localValue T
	var activeInbound string
	var inboundActivation capabilityActivation
	var pendingDelivery *deliveryRequest[T]
	var sendsBeforeDelivery int
	localClosed := false
	remoteClosed := false

	reportError := func(err error) {
		if err == nil {
			return
		}
		select {
		case channel.errors <- err:
		default:
		}
	}

	acknowledgeOutbound := func(err error) {
		for transferID, transfer := range outbound {
			transfer.activation.cancel()
			if transfer.reply != nil {
				transfer.reply <- err
			}
			delete(outbound, transferID)
		}
	}
	discardPreparedOutbound := func() {
		for _, prepared := range outboundFrames {
			session.releasePreparedBytes(prepared.reservedBytes)
		}
		outboundFrames = outboundFrames[:0]
	}
	defer acknowledgeOutbound(ErrChannelClosed)
	defer func() {
		discardPreparedOutbound()
		if writeLocal != nil {
			inboundActivation.cancel()
		}
		if pendingDelivery != nil {
			pendingDelivery.reply <- ErrChannelClosed
		}
		closeOwnedChannel(channel.outgoing)
		close(channel.incoming)
		close(channel.errors)
		close(channel.done)
	}()

	queueOutbound := func(message T, reply chan error) error {
		transferID, err := randomIdentifier()
		if err != nil {
			return err
		}
		encoded, err := prepareOwnedChannelValue(session, reflect.ValueOf(&message).Elem())
		if err != nil {
			return err
		}
		outbound[transferID] = outboundTransfer{
			reply: reply,
			activation: capabilityActivation{
				activate: encoded.activate,
				cancel:   encoded.cancel,
			},
		}
		outboundFrames = append(outboundFrames, preparedOutboundFrame{
			frame: networkFrame{
				Kind: frameChannelValue, ChannelID: channelID, TransferID: transferID, Payload: encoded.payload,
			},
			reservedBytes: uint64(len(encoded.payload)),
		})
		return nil
	}

	for {
		if pendingDelivery != nil && sendsBeforeDelivery > 0 && len(outbound) < maximumPendingFrames && len(outboundFrames) == 0 {
			select {
			case message, open := <-channel.outgoing:
				if !open {
					localClosed = true
					sendsBeforeDelivery = 0
				} else {
					if err := queueOutbound(message, nil); err != nil {
						reportError(err)
						failChannelBridge(session, channelID, err)
						return
					}
					sendsBeforeDelivery--
				}
			default:
				sendsBeforeDelivery = 0
			}
			continue
		}
		if pendingDelivery != nil && sendsBeforeDelivery == 0 && len(outbound) < maximumPendingFrames && len(outboundFrames) == 0 {
			request := pendingDelivery
			pendingDelivery = nil
			if err := queueOutbound(request.message, request.reply); err != nil {
				request.reply <- err
			}
			continue
		}

		if remoteClosed && writeLocal == nil && len(inboundQueue) == 0 {
			return
		}
		if localClosed && pendingDelivery == nil && len(outboundFrames) == 0 && len(outbound) == 0 {
			if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: channelID}); err != nil {
				reportError(err)
			}
			return
		}
		var dataCommands chan<- sessionSendCommand
		var dataCommand sessionSendCommand
		if len(outboundFrames) > 0 {
			dataCommands = session.dataSends
			dataCommand = sessionSendCommand{
				frame: outboundFrames[0].frame, reply: make(chan error, 1),
				reservedPayloadBytes: outboundFrames[0].reservedBytes,
			}
		}
		var readLocal <-chan T
		var strictRequests <-chan deliveryRequest[T]
		var decodeSlot <-chan struct{}
		if writeLocal == nil && len(inboundQueue) > 0 {
			decodeSlot = session.decodeSlots
		}
		if !localClosed && pendingDelivery == nil && len(outbound) < maximumPendingFrames && len(outboundFrames) == 0 {
			readLocal = channel.outgoing
			strictRequests = channel.deliveries
		}

		select {
		case <-decodeSlot:
			frame := inboundQueue[0]
			inboundQueue = inboundQueue[1:]
			decoded, err := prepareDecodedChannelValue(session, reflect.TypeOf((*T)(nil)).Elem(), frame.Payload)
			if err != nil {
				session.releaseDecodeSlot()
				reportError(err)
				failChannelBridge(session, channelID, err)
				return
			}
			activation, err := retainDecodedChannelValue(session, decoded)
			session.releaseDecodeSlot()
			if err != nil {
				reportError(err)
				failChannelBridge(session, channelID, err)
				return
			}
			activeInbound = frame.TransferID
			localValue = decoded.value.Interface().(T)
			inboundActivation = activation
			writeLocal = channel.incoming
			if err := session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: activeInbound}); err != nil {
				return
			}

		case message, open := <-readLocal:
			if !open {
				localClosed = true
			} else {
				if err := queueOutbound(message, nil); err != nil {
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
			}

		case request := <-strictRequests:
			pendingDelivery = &request
			sendsBeforeDelivery = len(channel.outgoing)

		case dataCommands <- dataCommand:
			prepared := outboundFrames[0]
			if err := <-dataCommand.reply; err != nil {
				session.releasePreparedBytes(prepared.reservedBytes)
				frame := outboundFrames[0].frame
				if transfer, exists := outbound[frame.TransferID]; exists {
					transfer.activation.cancel()
					if transfer.reply != nil {
						transfer.reply <- err
					} else {
						reportError(err)
					}
					delete(outbound, frame.TransferID)
				}
				outboundFrames = outboundFrames[1:]
				failChannelBridge(session, channelID, err)
				return
			}
			outboundFrames = outboundFrames[1:]

		case writeLocal <- localValue:
			writeLocal = nil
			inboundActivation.activate()
			inboundActivation = capabilityActivation{}
			delivered[activeInbound] = struct{}{}
			delete(inboundIdentifiers, activeInbound)
			if err := session.send(networkFrame{Kind: frameChannelDelivered, ChannelID: channelID, TransferID: activeInbound}); err != nil {
				return
			}
			activeInbound = ""

		case frame, open := <-incoming:
			if !open {
				return
			}
			switch frame.Kind {
			case frameChannelValue:
				if remoteClosed {
					err := fmt.Errorf("%w: peer sent a value after channel closure", ErrMalformedFrame)
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
				if _, exists := delivered[frame.TransferID]; exists {
					_ = session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: frame.TransferID})
					_ = session.send(networkFrame{Kind: frameChannelDelivered, ChannelID: channelID, TransferID: frame.TransferID})
					continue
				}
				if activeInbound == frame.TransferID {
					_ = session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: frame.TransferID})
					continue
				}
				if _, exists := inboundIdentifiers[frame.TransferID]; exists {
					continue
				}
				if len(inboundIdentifiers)+len(delivered) >= maximumPendingFrames {
					err := fmt.Errorf("%w: channel receive window is full", ErrPayloadLimit)
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
				inboundIdentifiers[frame.TransferID] = struct{}{}
				inboundQueue = append(inboundQueue, frame)

			case frameChannelPrepared:
				transfer, exists := outbound[frame.TransferID]
				if !exists || transfer.prepared {
					continue
				}
				transfer.prepared = true
				outbound[frame.TransferID] = transfer

			case frameChannelDelivered:
				transfer, exists := outbound[frame.TransferID]
				if !exists {
					continue
				}
				if !transfer.prepared {
					err := fmt.Errorf("%w: peer delivered a value before preparing its capabilities", ErrMalformedFrame)
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
				transfer.activation.activate()
				delete(outbound, frame.TransferID)
				if transfer.reply != nil {
					transfer.reply <- nil
				}
				if err := session.send(networkFrame{Kind: frameChannelReleased, ChannelID: channelID, TransferID: frame.TransferID}); err != nil {
					return
				}

			case frameChannelReleased:
				if remoteClosed {
					err := fmt.Errorf("%w: peer released a value after channel closure", ErrMalformedFrame)
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
				if _, exists := delivered[frame.TransferID]; !exists {
					err := fmt.Errorf("%w: peer released an unknown channel value", ErrMalformedFrame)
					reportError(err)
					failChannelBridge(session, channelID, err)
					return
				}
				delete(delivered, frame.TransferID)

			case frameChannelClosed:
				remoteClosed = true
				localClosed = true
				acknowledgeOutbound(ErrChannelClosed)
				discardPreparedOutbound()

			case frameChannelRevoked:
				if frame.Error != "" {
					reportError(errorFromNetworkFrame(frame))
				}
				acknowledgeOutbound(ErrChannelClosed)
				discardPreparedOutbound()
				if writeLocal != nil {
					inboundActivation.cancel()
					writeLocal = nil
					inboundActivation = capabilityActivation{}
				}
				return

			default:
				err := fmt.Errorf("%w: frame kind %d is invalid for a root channel", ErrMalformedFrame, frame.Kind)
				reportError(err)
				failChannelBridge(session, channelID, err)
				return
			}

		case err := <-session.errors:
			reportError(err)

		case <-session.done:
			for {
				select {
				case err := <-session.errors:
					reportError(err)
				default:
					return
				}
			}
		}
	}
}

type directionalInbound struct {
	identifier string
	value      reflect.Value
	activation capabilityActivation
}

func startDirectionalChannelBridge(session *session, channelID string, channel reflect.Value, direction channelDirection, original bool, incoming chan networkFrame) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDirectionalChannelBridge(session, channelID, channel, direction, original, incoming)
	}()
	return done
}

func runDirectionalChannelBridge(session *session, channelID string, channel reflect.Value, direction channelDirection, original bool, incoming chan networkFrame) {
	defer session.unregisterChannel(channelID)

	// The imported holder receives the declared right. The original bridge
	// performs its inverse so both processes still communicate with one channel.
	readLocal := original && direction == channelReceiveOnly || !original && direction == channelSendOnly
	writeLocal := original && direction == channelSendOnly || !original && direction == channelReceiveOnly
	outbound := make(map[string]outboundTransfer)
	outboundFrames := make([]preparedOutboundFrame, 0, 4)
	inboundIdentifiers := make(map[string]struct{})
	delivered := make(map[string]struct{})
	inboundQueue := make([]networkFrame, 0, 4)
	var activeInbound directionalInbound
	activeWrite := false
	localReadClosed := false
	remoteClosed := false
	closeSent := false
	closeLocalOnExit := false
	exportTerminal := false

	beginOriginalTerminal := func() error {
		if !original || exportTerminal {
			return nil
		}
		exportTerminal = true
		return session.beginExportCapabilityClose(channelID)
	}
	revokeChannel := func(err error) {
		if terminalErr := beginOriginalTerminal(); terminalErr != nil && !errors.Is(terminalErr, errSessionClosed) {
			session.publishError(terminalErr)
		}
		failChannelBridge(session, channelID, err)
	}

	defer func() {
		for _, prepared := range outboundFrames {
			session.releasePreparedBytes(prepared.reservedBytes)
		}
		for transferID, transfer := range outbound {
			transfer.activation.cancel()
			delete(outbound, transferID)
		}
		if activeWrite {
			activeInbound.activation.cancel()
		}
		if !original || closeLocalOnExit {
			closeReflectChannel(channel)
		}
	}()
	defer func() {
		if err := beginOriginalTerminal(); err != nil && !errors.Is(err, errSessionClosed) {
			session.publishError(err)
		}
	}()

	queueOutbound := func(value reflect.Value) error {
		transferID, err := randomIdentifier()
		if err != nil {
			return err
		}
		encoded, err := prepareOwnedChannelValue(session, value)
		if err != nil {
			return err
		}
		outbound[transferID] = outboundTransfer{
			activation: capabilityActivation{activate: encoded.activate, cancel: encoded.cancel},
		}
		outboundFrames = append(outboundFrames, preparedOutboundFrame{
			frame: networkFrame{
				Kind: frameChannelValue, ChannelID: channelID, TransferID: transferID, Payload: encoded.payload,
			},
			reservedBytes: uint64(len(encoded.payload)),
		})
		return nil
	}

	for {
		if remoteClosed && !activeWrite && len(inboundQueue) == 0 {
			if original {
				if err := beginOriginalTerminal(); err != nil {
					session.publishError(err)
					return
				}
				if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: channelID}); err != nil {
					session.publishError(err)
					return
				}
			}
			closeLocalOnExit = writeLocal
			return
		}
		if readLocal && localReadClosed && len(outboundFrames) == 0 && len(outbound) == 0 {
			if original {
				if err := beginOriginalTerminal(); err != nil {
					session.publishError(err)
					return
				}
				if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: channelID}); err != nil {
					session.publishError(err)
				}
				return
			}
			if !closeSent {
				if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: channelID}); err != nil {
					session.publishError(err)
					return
				}
				closeSent = true
			}
		}

		cases := make([]reflect.SelectCase, 0, 5)
		localReadCase := -1
		localWriteCase := -1
		dataSendCase := -1
		decodeSlotCase := -1
		var dataCommand sessionSendCommand

		if readLocal && !localReadClosed && len(outbound) < maximumPendingFrames && len(outboundFrames) == 0 {
			localReadCase = len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: channel})
		}
		if writeLocal && activeWrite {
			localWriteCase = len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectSend, Chan: channel, Send: activeInbound.value})
		}
		if len(outboundFrames) > 0 {
			dataSendCase = len(cases)
			dataCommand = sessionSendCommand{
				frame: outboundFrames[0].frame, reply: make(chan error, 1),
				reservedPayloadBytes: outboundFrames[0].reservedBytes,
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectSend, Chan: reflect.ValueOf(session.dataSends), Send: reflect.ValueOf(dataCommand)})
		}
		if writeLocal && !activeWrite && len(inboundQueue) > 0 {
			decodeSlotCase = len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(session.decodeSlots)})
		}
		incomingCase := len(cases)
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(incoming)})
		sessionDoneCase := len(cases)
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(session.done)})

		chosen, selected, open, err := selectChannelCases(cases)
		if err != nil {
			revokeChannel(err)
			return
		}
		switch chosen {
		case decodeSlotCase:
			frame := inboundQueue[0]
			inboundQueue = inboundQueue[1:]
			decoded, err := prepareDecodedChannelValue(session, channel.Type().Elem(), frame.Payload)
			if err != nil {
				session.releaseDecodeSlot()
				revokeChannel(err)
				closeLocalOnExit = writeLocal
				return
			}
			activation, err := retainDecodedChannelValue(session, decoded)
			session.releaseDecodeSlot()
			if err != nil {
				revokeChannel(err)
				closeLocalOnExit = writeLocal
				return
			}
			activeInbound = directionalInbound{
				identifier: frame.TransferID,
				value:      decoded.value,
				activation: activation,
			}
			activeWrite = true
			if err := session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: activeInbound.identifier}); err != nil {
				return
			}

		case localReadCase:
			if !open {
				localReadClosed = true
				continue
			}
			if err := queueOutbound(selected); err != nil {
				revokeChannel(err)
				return
			}

		case localWriteCase:
			activeWrite = false
			activeInbound.activation.activate()
			delivered[activeInbound.identifier] = struct{}{}
			delete(inboundIdentifiers, activeInbound.identifier)
			if err := session.send(networkFrame{Kind: frameChannelDelivered, ChannelID: channelID, TransferID: activeInbound.identifier}); err != nil {
				return
			}
			activeInbound = directionalInbound{}

		case dataSendCase:
			if err := <-dataCommand.reply; err != nil {
				session.releasePreparedBytes(outboundFrames[0].reservedBytes)
				outboundFrames = outboundFrames[1:]
				revokeChannel(err)
				return
			}
			outboundFrames = outboundFrames[1:]

		case incomingCase:
			if !open {
				closeLocalOnExit = !original && writeLocal
				return
			}
			frame := selected.Interface().(networkFrame)
			switch frame.Kind {
			case frameChannelValue:
				if remoteClosed || closeSent {
					revokeChannel(fmt.Errorf("%w: peer sent a value after channel closure", ErrMalformedFrame))
					closeLocalOnExit = writeLocal
					return
				}
				if !writeLocal {
					revokeChannel(fmt.Errorf("%w: peer sent a value without the send right", ErrMalformedFrame))
					return
				}
				if _, exists := delivered[frame.TransferID]; exists {
					_ = session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: frame.TransferID})
					_ = session.send(networkFrame{Kind: frameChannelDelivered, ChannelID: channelID, TransferID: frame.TransferID})
					continue
				}
				if activeWrite && activeInbound.identifier == frame.TransferID {
					_ = session.send(networkFrame{Kind: frameChannelPrepared, ChannelID: channelID, TransferID: frame.TransferID})
					continue
				}
				if _, exists := inboundIdentifiers[frame.TransferID]; exists {
					continue
				}
				if len(inboundIdentifiers)+len(delivered) >= maximumPendingFrames {
					revokeChannel(fmt.Errorf("%w: channel receive window is full", ErrPayloadLimit))
					closeLocalOnExit = writeLocal
					return
				}
				inboundIdentifiers[frame.TransferID] = struct{}{}
				inboundQueue = append(inboundQueue, frame)

			case frameChannelPrepared:
				if !readLocal {
					revokeChannel(fmt.Errorf("%w: peer prepared a value in the wrong channel direction", ErrMalformedFrame))
					return
				}
				transfer, exists := outbound[frame.TransferID]
				if !exists || transfer.prepared {
					continue
				}
				transfer.prepared = true
				outbound[frame.TransferID] = transfer

			case frameChannelDelivered:
				if !readLocal {
					revokeChannel(fmt.Errorf("%w: peer acknowledged a value in the wrong channel direction", ErrMalformedFrame))
					return
				}
				transfer, exists := outbound[frame.TransferID]
				if !exists {
					continue
				}
				if !transfer.prepared {
					revokeChannel(fmt.Errorf("%w: peer delivered a value before preparing its capabilities", ErrMalformedFrame))
					return
				}
				transfer.activation.activate()
				delete(outbound, frame.TransferID)
				if err := session.send(networkFrame{Kind: frameChannelReleased, ChannelID: channelID, TransferID: frame.TransferID}); err != nil {
					return
				}

			case frameChannelReleased:
				if remoteClosed || closeSent {
					revokeChannel(fmt.Errorf("%w: peer released a value after channel closure", ErrMalformedFrame))
					closeLocalOnExit = writeLocal
					return
				}
				if !writeLocal {
					revokeChannel(fmt.Errorf("%w: peer released a value in the wrong channel direction", ErrMalformedFrame))
					return
				}
				if _, exists := delivered[frame.TransferID]; !exists {
					revokeChannel(fmt.Errorf("%w: peer released an unknown channel value", ErrMalformedFrame))
					closeLocalOnExit = writeLocal
					return
				}
				delete(delivered, frame.TransferID)

			case frameChannelClosed:
				if !writeLocal && !(readLocal && !original && localReadClosed && closeSent) {
					revokeChannel(fmt.Errorf("%w: peer closed the wrong channel direction", ErrMalformedFrame))
					return
				}
				if readLocal && !original && localReadClosed && closeSent {
					return
				}
				if remoteClosed {
					revokeChannel(fmt.Errorf("%w: peer closed an already closed channel", ErrMalformedFrame))
					closeLocalOnExit = writeLocal
					return
				}
				remoteClosed = true

			case frameChannelRevoked:
				if frame.Error != "" {
					session.publishError(errorFromNetworkFrame(frame))
				}
				closeLocalOnExit = writeLocal
				return

			default:
				revokeChannel(fmt.Errorf("%w: frame kind %d is invalid for a directional channel", ErrMalformedFrame, frame.Kind))
				closeLocalOnExit = writeLocal
				return
			}

		case sessionDoneCase:
			closeLocalOnExit = !original && writeLocal
			return
		}
	}
}

func selectChannelCases(cases []reflect.SelectCase) (chosen int, selected reflect.Value, open bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: native channel operation failed: %v", ErrChannelClosed, recovered)
		}
	}()
	chosen, selected, open = reflect.Select(cases)
	return chosen, selected, open, nil
}

func closeReflectChannel(channel reflect.Value) {
	defer func() { _ = recover() }()
	channel.Close()
}

func schemaForType(valueType reflect.Type) schemaHash {
	descriptor := wireWriter{bytes: make([]byte, 0, 128)}
	writeNetworkTypeSchema(&descriptor, valueType, make(map[reflect.Type]uint32))
	return sha256.Sum256(descriptor.bytes)
}

func writeNetworkTypeSchema(writer *wireWriter, valueType reflect.Type, seen map[reflect.Type]uint32) {
	if identifier, exists := seen[valueType]; exists {
		writer.byte(0)
		writer.uint32(identifier)
		return
	}
	identifier := uint32(len(seen) + 1) // #nosec G115 -- a schema is bounded by the maximum wire frame before transmission.
	seen[valueType] = identifier
	writer.byte(1)
	writer.uint32(identifier)
	writer.byte(byte(valueType.Kind())) // #nosec G115 -- reflect.Kind is a byte-sized protocol enum.
	writer.data([]byte(valueType.PkgPath()), maximumWireFrameSize, "type package path")
	writer.data([]byte(valueType.Name()), maximumWireFrameSize, "type name")
	if usesBinaryCodec(valueType) {
		writer.byte(1)
		return
	}
	writer.byte(0)
	switch valueType.Kind() {
	case reflect.Pointer, reflect.Slice:
		writeNetworkTypeSchema(writer, valueType.Elem(), seen)
	case reflect.Array:
		writer.uint64(uint64(valueType.Len())) // #nosec G115 -- reflect array lengths are non-negative.
		writeNetworkTypeSchema(writer, valueType.Elem(), seen)
	case reflect.Map:
		writeNetworkTypeSchema(writer, valueType.Key(), seen)
		writeNetworkTypeSchema(writer, valueType.Elem(), seen)
	case reflect.Chan:
		writer.byte(byte(valueType.ChanDir())) // #nosec G115 -- reflect.ChanDir is a byte-sized protocol enum.
		writeNetworkTypeSchema(writer, valueType.Elem(), seen)
	case reflect.Struct:
		writer.uint32(uint32(valueType.NumField())) // #nosec G115 -- a schema is bounded by the maximum wire frame before transmission.
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			writer.data([]byte(field.Name), maximumWireFrameSize, "field name")
			writer.data([]byte(field.PkgPath), maximumWireFrameSize, "field package path")
			if field.Anonymous {
				writer.byte(1)
			} else {
				writer.byte(0)
			}
			writeNetworkTypeSchema(writer, field.Type, seen)
		}
	}
}

func failChannelBridge(session *session, channelID string, err error) {
	session.publishError(err)
	_ = session.send(networkFrame{Kind: frameChannelRevoked, ChannelID: channelID, Error: boundedProtocolError(err), ErrorCode: protocolErrorCodeFor(err)})
}

func boundedProtocolError(err error) string {
	message := err.Error()
	if len(message) > maximumWireErrorSize {
		message = message[:maximumWireErrorSize]
	}
	return message
}

func protocolErrorCodeFor(err error) protocolErrorCode {
	switch {
	case errors.Is(err, ErrUnsupportedType):
		return protocolErrorUnsupportedType
	case errors.Is(err, ErrSchemaMismatch):
		return protocolErrorSchemaMismatch
	case errors.Is(err, ErrMalformedFrame):
		return protocolErrorMalformedFrame
	case errors.Is(err, ErrPayloadLimit):
		return protocolErrorPayloadLimit
	case errors.Is(err, ErrChannelClosed):
		return protocolErrorChannelClosed
	default:
		return protocolErrorGeneral
	}
}

func errorFromNetworkFrame(frame networkFrame) error {
	var category error
	switch frame.ErrorCode {
	case protocolErrorUnsupportedType:
		category = ErrUnsupportedType
	case protocolErrorSchemaMismatch:
		category = ErrSchemaMismatch
	case protocolErrorMalformedFrame:
		category = ErrMalformedFrame
	case protocolErrorPayloadLimit:
		category = ErrPayloadLimit
	case protocolErrorChannelClosed:
		category = ErrChannelClosed
	default:
		return errors.New(frame.Error)
	}
	return fmt.Errorf("%w: %s", category, frame.Error)
}

const (
	maximumPayloadDepth     = 64
	maximumCollectionLength = 1 << 20
	maximumDecodeAllocation = 64 << 20
)

var (
	binaryMarshalerType   = reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem()
	binaryUnmarshalerType = reflect.TypeOf((*encoding.BinaryUnmarshaler)(nil)).Elem()
)

// encodeChannelValue writes one concrete Go value directly to its binary wire form.
// Channel descriptors are written inline, so decoding never needs attachment paths.
func encodeChannelValue(session *session, value reflect.Value) ([]byte, error) {
	if err := validateNetworkType(value.Type()); err != nil {
		return nil, err
	}
	return encodeValidatedChannelValue(session, value)
}

func encodeValidatedChannelValue(session *session, value reflect.Value) ([]byte, error) {
	prepared, err := prepareValidatedChannelValue(session, value)
	if err != nil {
		return nil, err
	}
	prepared.activate()
	return prepared.payload, nil
}

type capabilityActivation struct {
	activate func()
	cancel   func()
}

// preparedChannelValue keeps nested capabilities transactional with their
// parent value: delivery activates them, while cancellation unregisters them.
type preparedChannelValue struct {
	payload  []byte
	activate func()
	cancel   func()
}

// Encoding is serialized until its exact payload size has entered the session
// budget. This bounds temporary binary copies without coupling the later
// delivery order of independent network channels.
func prepareOwnedChannelValue(session *session, value reflect.Value) (preparedChannelValue, error) {
	if !session.acquireEncodeSlot() {
		return preparedChannelValue{}, errSessionClosed
	}
	defer session.releaseEncodeSlot()
	prepared, err := prepareValidatedChannelValue(session, value)
	if err != nil {
		return preparedChannelValue{}, err
	}
	if err := session.reservePreparedBytes(uint64(len(prepared.payload))); err != nil {
		prepared.cancel()
		return preparedChannelValue{}, err
	}
	return prepared, nil
}

func prepareValidatedChannelValue(session *session, value reflect.Value) (preparedChannelValue, error) {
	if uint64(value.Type().Size()) > maximumDecodeAllocation {
		return preparedChannelValue{}, fmt.Errorf("%w: value type exceeds allocation budget", ErrPayloadLimit)
	}
	encoder := binaryValueEncoder{
		session:        session,
		writer:         wireWriter{bytes: make([]byte, 0, 256)},
		activePointers: make(map[uintptr]struct{}),
		allocated:      uint64(value.Type().Size()),
	}
	encoder.encode(value, 0)
	payload, err := encoder.writer.result()
	if err == nil && len(payload) > maximumChannelPayloadSize {
		err = fmt.Errorf("%w: channel value exceeds maximum wire size", ErrPayloadLimit)
	}
	if err != nil {
		for _, capability := range encoder.capabilities {
			capability.cancel()
		}
		return preparedChannelValue{}, err
	}
	return preparedChannelValue{
		payload: payload,
		activate: func() {
			for _, capability := range encoder.capabilities {
				capability.activate()
			}
		},
		cancel: func() {
			for _, capability := range encoder.capabilities {
				capability.cancel()
			}
		},
	}, nil
}

type binaryValueEncoder struct {
	session         *session
	writer          wireWriter
	activePointers  map[uintptr]struct{}
	capabilities    []capabilityActivation
	allocated       uint64
	capabilityCount int
}

func (encoder *binaryValueEncoder) encode(value reflect.Value, depth int) {
	if encoder.writer.err != nil {
		return
	}
	if depth > maximumPayloadDepth {
		encoder.writer.fail(fmt.Errorf("%w: payload nesting exceeds maximum depth", ErrPayloadLimit))
		return
	}
	if usesBinaryCodec(value.Type()) {
		marshaler := value.Interface().(encoding.BinaryMarshaler)
		encoded, err := marshaler.MarshalBinary()
		if err != nil {
			encoder.writer.fail(fmt.Errorf("netchan: binary marshal %s: %w", value.Type(), err))
			return
		}
		encoder.reserve(uint64(len(encoded)))
		encoder.writer.data(encoded, maximumWireFrameSize, "binary value")
		return
	}

	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			encoder.writer.byte(1)
		} else {
			encoder.writer.byte(0)
		}
	case reflect.Int8:
		encoder.writer.byte(byte(int8(value.Int()))) // #nosec G115 -- the reflect kind fixes the width and the wire format preserves its bits.
	case reflect.Int16:
		encoder.writer.uint16(uint16(int16(value.Int()))) // #nosec G115 -- the reflect kind fixes the width and the wire format preserves its bits.
	case reflect.Int32:
		encoder.writer.uint32(uint32(int32(value.Int()))) // #nosec G115 -- the reflect kind fixes the width and the wire format preserves its bits.
	case reflect.Int, reflect.Int64:
		encoder.writer.uint64(uint64(value.Int())) // #nosec G115 -- two's-complement bits are preserved by the wire representation.
	case reflect.Uint8:
		encoder.writer.byte(byte(value.Uint())) // #nosec G115 -- the reflect kind guarantees an 8-bit unsigned value.
	case reflect.Uint16:
		encoder.writer.uint16(uint16(value.Uint())) // #nosec G115 -- the reflect kind guarantees a 16-bit unsigned value.
	case reflect.Uint32:
		encoder.writer.uint32(uint32(value.Uint())) // #nosec G115 -- the reflect kind guarantees a 32-bit unsigned value.
	case reflect.Uint, reflect.Uint64:
		encoder.writer.uint64(value.Uint())
	case reflect.Float32:
		encoder.writer.uint32(math.Float32bits(float32(value.Float())))
	case reflect.Float64:
		encoder.writer.uint64(math.Float64bits(value.Float()))
	case reflect.Complex64:
		complexValue := complex64(value.Complex())
		encoder.writer.uint32(math.Float32bits(real(complexValue)))
		encoder.writer.uint32(math.Float32bits(imag(complexValue)))
	case reflect.Complex128:
		complexValue := value.Complex()
		encoder.writer.uint64(math.Float64bits(real(complexValue)))
		encoder.writer.uint64(math.Float64bits(imag(complexValue)))
	case reflect.String:
		encoder.reserve(uint64(value.Len())) // #nosec G115 -- reflect lengths are non-negative.
		encoder.writer.data([]byte(value.String()), maximumWireFrameSize, "string")
	case reflect.Pointer:
		if value.IsNil() {
			encoder.writer.byte(0)
			return
		}
		pointer := value.Pointer()
		if _, exists := encoder.activePointers[pointer]; exists {
			encoder.writer.fail(errors.New("netchan: cyclic pointer value is not supported"))
			return
		}
		encoder.writer.byte(1)
		encoder.reserve(uint64(value.Type().Elem().Size()))
		encoder.activePointers[pointer] = struct{}{}
		encoder.encode(value.Elem(), depth+1)
		delete(encoder.activePointers, pointer)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				encoder.encode(value.Field(index), depth+1)
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			encoder.encode(value.Index(index), depth+1)
		}
	case reflect.Slice:
		if value.IsNil() {
			encoder.writer.byte(0)
			return
		}
		encoder.writer.byte(1)
		if !encoder.collection(value.Len(), value.Type().Elem().Size()) {
			return
		}
		encoder.writer.integer(value.Len(), "slice length")
		if value.Type().Elem().Kind() == reflect.Uint8 {
			encoder.writer.bytes = append(encoder.writer.bytes, value.Bytes()...)
			return
		}
		for index := 0; index < value.Len(); index++ {
			encoder.encode(value.Index(index), depth+1)
		}
	case reflect.Map:
		if value.IsNil() {
			encoder.writer.byte(0)
			return
		}
		encoder.writer.byte(1)
		estimatedEntrySize := value.Type().Key().Size() + value.Type().Elem().Size() + 16
		if !encoder.collection(value.Len(), estimatedEntrySize) {
			return
		}
		encoder.writer.integer(value.Len(), "map length")
		iterator := value.MapRange()
		for iterator.Next() {
			encoder.encode(iterator.Key(), depth+1)
			encoder.encode(iterator.Value(), depth+1)
		}
	case reflect.Chan:
		encoder.encodeChannel(value)
	default:
		encoder.writer.fail(fmt.Errorf("netchan: unsupported value kind %s", value.Kind()))
	}
}

func (encoder *binaryValueEncoder) collection(length int, elementSize uintptr) bool {
	if length < 0 || length > maximumCollectionLength {
		encoder.writer.fail(fmt.Errorf("%w: collection exceeds maximum length", ErrPayloadLimit))
		return false
	}
	encoder.reserve(uint64(length) * uint64(elementSize))
	return encoder.writer.err == nil
}

func (encoder *binaryValueEncoder) reserve(size uint64) {
	if size > maximumDecodeAllocation || encoder.allocated > maximumDecodeAllocation-size {
		encoder.writer.fail(fmt.Errorf("%w: payload exceeds allocation budget", ErrPayloadLimit))
		return
	}
	encoder.allocated += size
}

func (encoder *binaryValueEncoder) encodeChannel(channel reflect.Value) {
	if channel.IsNil() {
		encoder.writer.byte(0)
		return
	}
	if channel.Type().ChanDir() == reflect.BothDir {
		encoder.writer.fail(fmt.Errorf("%w: nested channels must declare a send or receive direction", ErrUnsupportedType))
		return
	}
	if err := validateChannelCapacity(channel.Cap(), channel.Type().Elem()); err != nil {
		encoder.writer.fail(err)
		return
	}
	if encoder.session == nil {
		encoder.writer.fail(errors.New("netchan: a channel capability requires a network session"))
		return
	}
	if encoder.capabilityCount >= maximumValueCapabilities {
		encoder.writer.fail(fmt.Errorf("%w: value contains too many channel capabilities", ErrPayloadLimit))
		return
	}
	encoder.capabilityCount++
	encoder.reserve(uint64(channel.Cap()) * uint64(channel.Type().Elem().Size())) // #nosec G115 -- native channel capacity is non-negative.
	if encoder.writer.err != nil {
		return
	}
	encoder.writer.byte(1)
	identity := channel.Pointer()
	direction := directionFromReflect(channel.Type().ChanDir())
	schema := schemaForType(channel.Type())
	proposedIdentifier, err := randomIdentifier()
	if err != nil {
		encoder.writer.fail(err)
		return
	}
	reservation, incoming, err := encoder.session.reserveExportCapability(channelCapabilityIdentity{
		pointer: identity, direction: direction, schema: schema,
	}, channel, proposedIdentifier)
	if err != nil {
		encoder.writer.fail(err)
		return
	}
	encoder.capabilities = append(encoder.capabilities, capabilityActivation{
		activate: func() {
			if encoder.session.commitExportCapability(reservation) {
				startDirectionalChannelBridge(encoder.session, reservation.identifier, channel, direction, true, incoming)
			}
		},
		cancel: func() {
			encoder.session.cancelExportCapability(reservation)
		},
	})
	encoder.writer.string(reservation.identifier, maximumWireIdentifierSize, "channel identifier")
	encoder.writer.byte(byte(direction))
	encoder.writer.integer(channel.Cap(), "channel capacity")
	encoder.writer.bytes = append(encoder.writer.bytes, schema[:]...)
}

func decodeChannelValue(session *session, elementType reflect.Type, payload []byte) (reflect.Value, error) {
	if err := validateNetworkType(elementType); err != nil {
		return reflect.Value{}, err
	}
	return decodeValidatedChannelValue(session, elementType, payload)
}

func decodeValidatedChannelValue(session *session, elementType reflect.Type, payload []byte) (reflect.Value, error) {
	decoded, err := prepareDecodedChannelValue(session, elementType, payload)
	if err != nil {
		return reflect.Value{}, err
	}
	decoded.activation.activate()
	return decoded.value, nil
}

type preparedDecodedChannelValue struct {
	value      reflect.Value
	activation capabilityActivation
	allocation uint64
}

func prepareDecodedChannelValue(session *session, elementType reflect.Type, payload []byte) (preparedDecodedChannelValue, error) {
	if uint64(elementType.Size()) > maximumDecodeAllocation {
		return preparedDecodedChannelValue{}, fmt.Errorf("%w: value type exceeds allocation budget", ErrPayloadLimit)
	}
	decoder := binaryValueDecoder{
		session:   session,
		reader:    wireReader{bytes: payload},
		proxies:   make(map[string]decodedChannelCapability),
		allocated: uint64(elementType.Size()),
	}
	value := reflect.New(elementType).Elem()
	decoder.decode(value, 0)
	if err := decoder.reader.finish(); err != nil {
		decoder.rollbackImportedCapabilities()
		return preparedDecodedChannelValue{}, err
	}
	return preparedDecodedChannelValue{
		value:      value,
		allocation: decoder.allocated,
		activation: capabilityActivation{
			activate: func() {
				decoder.activateImportedCapabilities()
				if value.CanAddr() && value.Addr().CanInterface() {
					if lifecycle, ok := value.Addr().Interface().(networkValueLifecycle); ok {
						lifecycle.startNetworkLifecycle()
					}
				}
			},
			cancel: decoder.rollbackImportedCapabilities,
		},
	}, nil
}

// Decoding is serialized only while memory is being allocated. A separate
// bounded ownership budget then permits independent channel processes to keep
// prepared values without making their application-level receive order global.
func retainDecodedChannelValue(session *session, decoded preparedDecodedChannelValue) (capabilityActivation, error) {
	if err := session.reserveDecodedBytes(decoded.allocation); err != nil {
		decoded.activation.cancel()
		return capabilityActivation{}, err
	}
	return capabilityActivation{
		activate: func() {
			decoded.activation.activate()
			session.releaseDecodedBytes(decoded.allocation)
		},
		cancel: func() {
			decoded.activation.cancel()
			session.releaseDecodedBytes(decoded.allocation)
		},
	}, nil
}

type binaryValueDecoder struct {
	session         *session
	reader          wireReader
	proxies         map[string]decodedChannelCapability
	pendingImports  []pendingImportedCapability
	allocated       uint64
	capabilityCount int
}

type decodedChannelCapability struct {
	channel     reflect.Value
	channelType reflect.Type
	capacity    int
}

type pendingImportedCapability struct {
	reservation capabilityReservation
	channel     reflect.Value
	direction   channelDirection
	incoming    chan networkFrame
}

func (decoder *binaryValueDecoder) activateImportedCapabilities() {
	for _, capability := range decoder.pendingImports {
		if decoder.session.commitImportCapability(capability.reservation) {
			startDirectionalChannelBridge(decoder.session, capability.reservation.identifier, capability.channel, capability.direction, false, capability.incoming)
		}
	}
	decoder.pendingImports = nil
}

func (decoder *binaryValueDecoder) rollbackImportedCapabilities() {
	for _, capability := range decoder.pendingImports {
		decoder.session.cancelImportCapability(capability.reservation)
	}
	decoder.pendingImports = nil
}

func (decoder *binaryValueDecoder) decode(destination reflect.Value, depth int) {
	if decoder.reader.err != nil {
		return
	}
	if depth > maximumPayloadDepth {
		decoder.reader.fail(fmt.Errorf("%w: payload nesting exceeds maximum depth", ErrPayloadLimit))
		return
	}
	if usesBinaryCodec(destination.Type()) {
		encoded := decoder.reader.data(maximumWireFrameSize, "binary value")
		decoder.reserve(uint64(len(encoded)))
		if decoder.reader.err != nil {
			return
		}
		unmarshaler := destination.Addr().Interface().(encoding.BinaryUnmarshaler)
		if err := unmarshaler.UnmarshalBinary(encoded); err != nil {
			decoder.reader.fail(fmt.Errorf("netchan: binary unmarshal %s: %w", destination.Type(), err))
		}
		return
	}

	switch destination.Kind() {
	case reflect.Bool:
		encoded := decoder.reader.byte()
		if encoded > 1 {
			decoder.reader.fail(errors.New("netchan: invalid boolean value"))
			return
		}
		destination.SetBool(encoded == 1)
	case reflect.Int8:
		destination.SetInt(int64(int8(decoder.reader.byte()))) // #nosec G115 -- decoding restores the signed value from its wire bits.
	case reflect.Int16:
		destination.SetInt(int64(int16(decoder.reader.uint16()))) // #nosec G115 -- decoding restores the signed value from its wire bits.
	case reflect.Int32:
		destination.SetInt(int64(int32(decoder.reader.uint32()))) // #nosec G115 -- decoding restores the signed value from its wire bits.
	case reflect.Int, reflect.Int64:
		encoded := int64(decoder.reader.uint64()) // #nosec G115 -- decoding restores the signed value from its wire bits.
		if destination.OverflowInt(encoded) {
			decoder.reader.fail(fmt.Errorf("netchan: integer overflows %s", destination.Type()))
			return
		}
		destination.SetInt(encoded)
	case reflect.Uint8:
		destination.SetUint(uint64(decoder.reader.byte()))
	case reflect.Uint16:
		destination.SetUint(uint64(decoder.reader.uint16()))
	case reflect.Uint32:
		destination.SetUint(uint64(decoder.reader.uint32()))
	case reflect.Uint, reflect.Uint64:
		encoded := decoder.reader.uint64()
		if destination.OverflowUint(encoded) {
			decoder.reader.fail(fmt.Errorf("netchan: unsigned integer overflows %s", destination.Type()))
			return
		}
		destination.SetUint(encoded)
	case reflect.Float32:
		destination.SetFloat(float64(math.Float32frombits(decoder.reader.uint32())))
	case reflect.Float64:
		destination.SetFloat(math.Float64frombits(decoder.reader.uint64()))
	case reflect.Complex64:
		realPart := math.Float32frombits(decoder.reader.uint32())
		imaginaryPart := math.Float32frombits(decoder.reader.uint32())
		destination.SetComplex(complex(float64(realPart), float64(imaginaryPart)))
	case reflect.Complex128:
		realPart := math.Float64frombits(decoder.reader.uint64())
		imaginaryPart := math.Float64frombits(decoder.reader.uint64())
		destination.SetComplex(complex(realPart, imaginaryPart))
	case reflect.String:
		encoded := decoder.reader.data(maximumWireFrameSize, "string")
		decoder.reserve(uint64(len(encoded)))
		if decoder.reader.err == nil {
			destination.SetString(string(encoded))
		}
	case reflect.Pointer:
		if !decoder.present("pointer") {
			return
		}
		decoder.reserve(uint64(destination.Type().Elem().Size()))
		if decoder.reader.err != nil {
			return
		}
		destination.Set(reflect.New(destination.Type().Elem()))
		decoder.decode(destination.Elem(), depth+1)
	case reflect.Struct:
		for index := 0; index < destination.NumField(); index++ {
			if destination.Type().Field(index).PkgPath == "" {
				decoder.decode(destination.Field(index), depth+1)
			}
		}
	case reflect.Array:
		for index := 0; index < destination.Len(); index++ {
			decoder.decode(destination.Index(index), depth+1)
		}
	case reflect.Slice:
		if !decoder.present("slice") {
			return
		}
		length := decoder.reader.integer("slice length")
		if !decoder.collection(length, destination.Type().Elem().Size()) {
			return
		}
		if destination.Type().Elem().Kind() == reflect.Uint8 {
			encoded := decoder.reader.take(length, "byte slice")
			if decoder.reader.err == nil {
				destination.SetBytes(append([]byte(nil), encoded...))
			}
			return
		}
		destination.Set(reflect.MakeSlice(destination.Type(), length, length))
		for index := 0; index < length; index++ {
			decoder.decode(destination.Index(index), depth+1)
		}
	case reflect.Map:
		if !decoder.present("map") {
			return
		}
		length := decoder.reader.integer("map length")
		estimatedEntrySize := destination.Type().Key().Size() + destination.Type().Elem().Size() + 16
		if !decoder.collection(length, estimatedEntrySize) {
			return
		}
		destination.Set(reflect.MakeMapWithSize(destination.Type(), length))
		for index := 0; index < length; index++ {
			key := reflect.New(destination.Type().Key()).Elem()
			mapValue := reflect.New(destination.Type().Elem()).Elem()
			decoder.decode(key, depth+1)
			decoder.decode(mapValue, depth+1)
			if decoder.reader.err == nil {
				destination.SetMapIndex(key, mapValue)
			}
		}
	case reflect.Chan:
		decoder.decodeChannel(destination)
	default:
		decoder.reader.fail(fmt.Errorf("netchan: unsupported destination kind %s", destination.Kind()))
	}
}

func (decoder *binaryValueDecoder) present(name string) bool {
	marker := decoder.reader.byte()
	if marker > 1 {
		decoder.reader.fail(fmt.Errorf("netchan: invalid %s marker", name))
		return false
	}
	return marker == 1
}

func (decoder *binaryValueDecoder) collection(length int, elementSize uintptr) bool {
	if length < 0 || length > maximumCollectionLength {
		decoder.reader.fail(fmt.Errorf("%w: collection exceeds maximum length", ErrPayloadLimit))
		return false
	}
	decoder.reserve(uint64(length) * uint64(elementSize))
	return decoder.reader.err == nil
}

func (decoder *binaryValueDecoder) reserve(size uint64) {
	if size > maximumDecodeAllocation || decoder.allocated > maximumDecodeAllocation-size {
		decoder.reader.fail(fmt.Errorf("%w: payload exceeds decode allocation budget", ErrPayloadLimit))
		return
	}
	decoder.allocated += size
}

func (decoder *binaryValueDecoder) decodeChannel(destination reflect.Value) {
	if !decoder.present("channel") {
		return
	}
	identifier := decoder.reader.string(maximumWireIdentifierSize, "channel identifier")
	direction := channelDirection(decoder.reader.byte())
	capacity := decoder.reader.integer("channel capacity")
	var schema schemaHash
	copy(schema[:], decoder.reader.take(sha256.Size, "channel schema"))
	if decoder.reader.err != nil {
		return
	}
	if identifier == "" {
		decoder.reader.fail(fmt.Errorf("%w: channel capability has no identifier", ErrMalformedFrame))
		return
	}
	if destination.Type().ChanDir() == reflect.BothDir {
		decoder.reader.fail(fmt.Errorf("%w: nested channels must declare a send or receive direction", ErrUnsupportedType))
		return
	}
	if err := validateChannelCapacity(capacity, destination.Type().Elem()); err != nil {
		decoder.reader.fail(err)
		return
	}
	decoder.reserve(uint64(capacity) * uint64(destination.Type().Elem().Size())) // #nosec G115 -- capacity is validated as non-negative above.
	if !validChannelDirection(direction) || direction != directionFromReflect(destination.Type().ChanDir()) {
		decoder.reader.fail(errors.New("netchan: channel capability has an invalid direction"))
		return
	}
	if schemaForType(destination.Type()) != schema {
		decoder.reader.fail(fmt.Errorf("%w: attached channel does not match destination type", ErrSchemaMismatch))
		return
	}
	if decoder.session == nil {
		decoder.reader.fail(errors.New("netchan: a channel capability requires a network session"))
		return
	}
	if decoder.capabilityCount >= maximumValueCapabilities {
		decoder.reader.fail(fmt.Errorf("%w: value contains too many channel capabilities", ErrPayloadLimit))
		return
	}
	decoder.capabilityCount++
	capability, exists := decoder.proxies[identifier]
	if exists && (capability.channelType != destination.Type() || capability.capacity != capacity) {
		decoder.reader.fail(fmt.Errorf("%w: channel capability changed its type or capacity", ErrMalformedFrame))
		return
	}
	reservation, proxy, incoming, err := decoder.session.reserveImportCapability(identifier, destination.Type(), capacity)
	if err != nil {
		decoder.reader.fail(err)
		return
	}
	if exists && proxy.Pointer() != capability.channel.Pointer() {
		decoder.session.cancelImportCapability(reservation)
		decoder.reader.fail(fmt.Errorf("%w: channel capability identity changed", ErrMalformedFrame))
		return
	}
	if !exists {
		decoder.proxies[identifier] = decodedChannelCapability{channel: proxy, channelType: destination.Type(), capacity: capacity}
	}
	decoder.pendingImports = append(decoder.pendingImports, pendingImportedCapability{
		reservation: reservation,
		channel:     proxy,
		direction:   direction,
		incoming:    incoming,
	})
	destination.Set(proxy.Convert(destination.Type()))
}

func usesBinaryCodec(valueType reflect.Type) bool {
	if valueType.Kind() == reflect.Pointer ||
		!valueType.Implements(binaryMarshalerType) ||
		!reflect.PointerTo(valueType).Implements(binaryUnmarshalerType) {
		return false
	}
	return !typeContainsChannel(valueType, make(map[reflect.Type]bool))
}

func validateNetworkType(valueType reflect.Type) error {
	return validateNetworkTypeAt(valueType, make(map[reflect.Type]bool))
}

func validateNetworkTypeAt(valueType reflect.Type, visiting map[reflect.Type]bool) error {
	if uint64(valueType.Size()) > maximumDecodeAllocation {
		return fmt.Errorf("%w: type %s exceeds allocation budget", ErrPayloadLimit, valueType)
	}
	if visiting[valueType] {
		return nil
	}
	visiting[valueType] = true
	defer delete(visiting, valueType)
	if usesBinaryCodec(valueType) {
		return nil
	}
	switch valueType.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return nil
	case reflect.Array:
		if valueType.Len() > maximumCollectionLength {
			return fmt.Errorf("%w: array type %s exceeds maximum length", ErrPayloadLimit, valueType)
		}
		return validateNetworkTypeAt(valueType.Elem(), visiting)
	case reflect.Pointer, reflect.Slice:
		return validateNetworkTypeAt(valueType.Elem(), visiting)
	case reflect.Chan:
		if valueType.ChanDir() == reflect.BothDir {
			return fmt.Errorf("%w: nested channels must declare a send or receive direction", ErrUnsupportedType)
		}
		return validateNetworkTypeAt(valueType.Elem(), visiting)
	case reflect.Map:
		if typeContainsChannel(valueType.Key(), make(map[reflect.Type]bool)) {
			return fmt.Errorf("%w: channels as map keys are not supported", ErrUnsupportedType)
		}
		if err := validateNetworkTypeAt(valueType.Key(), visiting); err != nil {
			return err
		}
		return validateNetworkTypeAt(valueType.Elem(), visiting)
	case reflect.Struct:
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			if field.PkgPath != "" {
				return fmt.Errorf("%w: unexported field %s.%s requires an explicit binary codec", ErrUnsupportedType, valueType, field.Name)
			}
			if err := validateNetworkTypeAt(field.Type, visiting); err != nil {
				return err
			}
		}
		return nil
	case reflect.Interface:
		return fmt.Errorf("%w: interface type %s requires a concrete channel element type", ErrUnsupportedType, valueType)
	default:
		return fmt.Errorf("%w: type %s", ErrUnsupportedType, valueType)
	}
}

func validateChannelCapacity(capacity int, elementType reflect.Type) error {
	if capacity < 0 || capacity > maximumCollectionLength {
		return fmt.Errorf("%w: channel capacity exceeds maximum length", ErrPayloadLimit)
	}
	elementSize := uint64(elementType.Size())
	if elementSize != 0 && uint64(capacity) > maximumDecodeAllocation/elementSize {
		return fmt.Errorf("%w: channel capacity exceeds allocation budget", ErrPayloadLimit)
	}
	return nil
}

func typeContainsChannel(valueType reflect.Type, visiting map[reflect.Type]bool) bool {
	if visiting[valueType] {
		return false
	}
	visiting[valueType] = true
	defer delete(visiting, valueType)
	switch valueType.Kind() {
	case reflect.Chan:
		return true
	case reflect.Pointer, reflect.Array, reflect.Slice:
		return typeContainsChannel(valueType.Elem(), visiting)
	case reflect.Map:
		return typeContainsChannel(valueType.Key(), visiting) ||
			typeContainsChannel(valueType.Elem(), visiting)
	case reflect.Struct:
		for index := 0; index < valueType.NumField(); index++ {
			if typeContainsChannel(valueType.Field(index).Type, visiting) {
				return true
			}
		}
	}
	return false
}

func directionFromReflect(direction reflect.ChanDir) channelDirection {
	switch direction {
	case reflect.SendDir:
		return channelSendOnly
	case reflect.RecvDir:
		return channelReceiveOnly
	default:
		return channelBidirectional
	}
}

type networkValueLifecycle interface {
	startNetworkLifecycle()
}

////////////////////////////////////////////////////////////////////////////////
// END: Native channel bridges and channel capabilities
////////////////////////////////////////////////////////////////////////////////
