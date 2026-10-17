// Package packet turns a bounded, unreliable duplex packet link into a stream.
// The actor owns sequencing, queues, timers and cancellation. Link adapters own
// native callbacks and never expose their mutable state to the protocol core.
package packet

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

const (
	headerSize    = 9
	flagData      = 0x11
	flagAck       = 0x12
	flagFinish    = 0x14
	maximumWindow = 8
	maximumWrite  = 1024
)

// Link transfers ownership of every received packet to its consumer. Send must
// stop when done closes; overflow must terminate the link rather than lose bytes
// silently. MTU is the available payload after the native adapter's envelope.
type Link interface {
	Send(payload []byte, done <-chan struct{}) error
	Packets() <-chan []byte
	Done() <-chan struct{}
	MTU() int
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

type Stream struct {
	link     Link
	reads    chan readRequest
	writes   chan writeRequest
	commands chan any
	done     chan struct{}
	activity chan struct{}
}

type readRequest struct {
	maximum int
	reply   chan readResult
}
type readResult struct {
	payload []byte
	err     error
}
type writeRequest struct {
	payload []byte
	reply   chan writeResult
}
type writeResult struct {
	count int
	err   error
}
type deadlineRequest struct {
	read, write bool
	deadline    time.Time
	reply       chan error
}
type closeRequest struct{}
type drainRequest struct {
	deadline time.Time
	reply    chan error
}
type pendingPacket struct {
	sequence uint32
	payload  []byte
	finish   bool
	sent     time.Time
}
type receivePacket struct {
	payload []byte
	finish  bool
}

func Open(link Link) (*Stream, error) {
	if link.MTU() <= headerSize || link.MTU() > 65535 {
		return nil, errors.New("netchan: invalid packet link MTU")
	}
	stream := &Stream{link: link, reads: make(chan readRequest), writes: make(chan writeRequest), commands: make(chan any), done: make(chan struct{}), activity: make(chan struct{}, 1)}
	go stream.run()
	return stream, nil
}

func (stream *Stream) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	reply := make(chan readResult, 1)
	select {
	case stream.reads <- readRequest{maximum: len(destination), reply: reply}:
	case <-stream.done:
		return 0, net.ErrClosed
	}
	select {
	case result := <-reply:
		return copy(destination, result.payload), result.err
	case <-stream.done:
		return 0, net.ErrClosed
	}
}

func (stream *Stream) Write(payload []byte) (int, error) {
	written := 0
	for len(payload) > 0 {
		length := len(payload)
		if length > maximumWrite {
			length = maximumWrite
		}
		reply := make(chan writeResult, 1)
		request := writeRequest{payload: append([]byte(nil), payload[:length]...), reply: reply}
		select {
		case stream.writes <- request:
		case <-stream.done:
			return written, net.ErrClosed
		}
		select {
		case result := <-reply:
			written += result.count
			if result.err != nil {
				return written, result.err
			}
		case <-stream.done:
			return written, net.ErrClosed
		}
		payload = payload[length:]
	}
	return written, nil
}

func (stream *Stream) Close() error {
	select {
	case stream.commands <- closeRequest{}:
	case <-stream.done:
	}
	<-stream.done
	return nil
}
func (stream *Stream) LocalAddr() net.Addr       { return stream.link.LocalAddr() }
func (stream *Stream) RemoteAddr() net.Addr      { return stream.link.RemoteAddr() }
func (stream *Stream) Activity() <-chan struct{} { return stream.activity }
func (stream *Stream) SetDeadline(deadline time.Time) error {
	return stream.deadline(deadline, true, true)
}
func (stream *Stream) SetReadDeadline(deadline time.Time) error {
	return stream.deadline(deadline, true, false)
}
func (stream *Stream) SetWriteDeadline(deadline time.Time) error {
	return stream.deadline(deadline, false, true)
}
func (stream *Stream) deadline(deadline time.Time, read, write bool) error {
	reply := make(chan error, 1)
	select {
	case stream.commands <- deadlineRequest{read: read, write: write, deadline: deadline, reply: reply}:
	case <-stream.done:
		return net.ErrClosed
	}
	select {
	case err := <-reply:
		return err
	case <-stream.done:
		return net.ErrClosed
	}
}

func (stream *Stream) Drain(deadline time.Time) error {
	reply := make(chan error, 1)
	select {
	case stream.commands <- drainRequest{deadline: deadline, reply: reply}:
	case <-stream.done:
		return net.ErrClosed
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err := <-reply:
		return err
	case <-stream.done:
		return net.ErrClosed
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func encode(flags byte, sequence, acknowledgement uint32, payload []byte) []byte {
	encoded := make([]byte, headerSize+len(payload))
	encoded[0] = flags
	binary.BigEndian.PutUint32(encoded[1:5], sequence)
	binary.BigEndian.PutUint32(encoded[5:9], acknowledgement)
	copy(encoded[headerSize:], payload)
	return encoded
}

func (stream *Stream) run() {
	send := make(chan []byte)
	sent := make(chan error, 1)
	go func() {
		for {
			select {
			case payload := <-send:
				err := stream.link.Send(payload, stream.done)
				select {
				case sent <- err:
				case <-stream.done:
					return
				}
				if err != nil {
					return
				}
			case <-stream.done:
				return
			}
		}
	}()
	defer func() { close(stream.done); _ = stream.link.Close() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var readDeadline, writeDeadline time.Time
	var reads []readRequest
	var activeWrite *writeRequest
	var drain *drainRequest
	var writeOffset, writeAcknowledged int
	var buffered []byte
	pending := make([]pendingPacket, 0, maximumWindow)
	reordered := make(map[uint32]receivePacket)
	var outgoingSequence uint32 = 1
	var incomingSequence uint32 = 1
	var lastAcknowledged uint32
	var acknowledgement []byte
	var sending bool
	var remoteFinished, localFinished bool
	var terminalError error = net.ErrClosed
	defer func() {
		for _, request := range reads {
			request.reply <- readResult{err: terminalError}
		}
		if activeWrite != nil {
			activeWrite.reply <- writeResult{count: writeAcknowledged, err: terminalError}
		}
		if drain != nil {
			drain.reply <- terminalError
		}
	}()
	for {
		for len(reads) > 0 && (len(buffered) > 0 || remoteFinished || !readDeadline.IsZero() && !time.Now().Before(readDeadline)) {
			request := reads[0]
			reads = reads[1:]
			switch {
			case !readDeadline.IsZero() && !time.Now().Before(readDeadline):
				request.reply <- readResult{err: os.ErrDeadlineExceeded}
			case len(buffered) > 0:
				length := len(buffered)
				if length > request.maximum {
					length = request.maximum
				}
				request.reply <- readResult{payload: append([]byte(nil), buffered[:length]...)}
				buffered = buffered[length:]
			default:
				request.reply <- readResult{err: io.EOF}
			}
		}
		// Reassembly advances only when the stream has room. Withholding ACKs
		// applies backpressure before another packet can consume memory.
		for len(buffered)+stream.link.MTU() <= maximumBufferedBytes {
			received, exists := reordered[incomingSequence]
			if !exists {
				break
			}
			delete(reordered, incomingSequence)
			buffered = append(buffered, received.payload...)
			remoteFinished = remoteFinished || received.finish
			incomingSequence++
			acknowledgement = encode(flagAck, 0, incomingSequence-1, nil)
			select {
			case stream.activity <- struct{}{}:
			default:
			}
		}
		if len(reads) > 0 && (len(buffered) > 0 || remoteFinished) {
			continue
		}
		if activeWrite != nil && writeOffset == len(activeWrite.payload) && len(pending) == 0 {
			activeWrite.reply <- writeResult{count: writeAcknowledged}
			activeWrite = nil
		}
		if activeWrite != nil {
			for writeOffset < len(activeWrite.payload) && len(pending) < maximumWindow {
				length := len(activeWrite.payload) - writeOffset
				if length > stream.link.MTU()-headerSize {
					length = stream.link.MTU() - headerSize
				}
				pending = append(pending, pendingPacket{sequence: outgoingSequence, payload: activeWrite.payload[writeOffset : writeOffset+length]})
				outgoingSequence++
				writeOffset += length
				if outgoingSequence == 0 {
					terminalError = errors.New("netchan: packet sequence exhausted")
					return
				}
			}
		}
		if drain != nil && activeWrite == nil && !localFinished && len(pending) == 0 {
			pending = append(pending, pendingPacket{sequence: outgoingSequence, finish: true})
			outgoingSequence++
			localFinished = true
		}
		if drain != nil && localFinished && len(pending) == 0 {
			drain.reply <- nil
			drain = nil
		}
		var networkSend chan []byte
		var networkPacket []byte
		selectedPending := -1
		if !sending {
			if acknowledgement != nil {
				networkPacket = acknowledgement
			} else {
				for index, packet := range pending {
					if packet.sent.IsZero() || time.Since(packet.sent) >= 250*time.Millisecond {
						flags := byte(flagData)
						if packet.finish {
							flags = flagFinish
						}
						networkPacket = encode(flags, packet.sequence, incomingSequence-1, packet.payload)
						selectedPending = index
						break
					}
				}
			}
			if networkPacket != nil {
				networkSend = send
			}
		}
		var writes chan writeRequest
		if activeWrite == nil && drain == nil {
			writes = stream.writes
		}
		select {
		case networkSend <- networkPacket:
			sending = true
			if selectedPending >= 0 {
				pending[selectedPending].sent = time.Now()
			} else {
				acknowledgement = nil
			}
		case err := <-sent:
			sending = false
			if err != nil {
				terminalError = err
				return
			}
		case request := <-writes:
			if localFinished {
				request.reply <- writeResult{err: io.ErrClosedPipe}
				continue
			}
			if !writeDeadline.IsZero() && !time.Now().Before(writeDeadline) {
				request.reply <- writeResult{err: os.ErrDeadlineExceeded}
				continue
			}
			activeWrite = &request
			writeOffset, writeAcknowledged = 0, 0
		case request := <-stream.reads:
			if len(reads) >= maximumWindow {
				request.reply <- readResult{err: errors.New("netchan: concurrent read limit exceeded")}
			} else {
				reads = append(reads, request)
			}
		case command := <-stream.commands:
			switch request := command.(type) {
			case closeRequest:
				return
			case deadlineRequest:
				if request.read {
					readDeadline = request.deadline
				}
				if request.write {
					writeDeadline = request.deadline
				}
				request.reply <- nil
			case drainRequest:
				if drain != nil {
					request.reply <- errors.New("netchan: stream already draining")
				} else {
					drain = &request
				}
			}
		case payload, open := <-stream.link.Packets():
			if !open {
				terminalError = io.EOF
				return
			}
			if len(payload) < headerSize || len(payload) > stream.link.MTU() {
				terminalError = errors.New("netchan: malformed packet length")
				return
			}
			flags := payload[0]
			if flags != flagData && flags != flagAck && flags != flagFinish {
				terminalError = errors.New("netchan: invalid packet flags")
				return
			}
			sequence := binary.BigEndian.Uint32(payload[1:5])
			ack := binary.BigEndian.Uint32(payload[5:9])
			if ack >= outgoingSequence {
				terminalError = errors.New("netchan: impossible packet acknowledgement")
				return
			}
			if ack > lastAcknowledged {
				lastAcknowledged = ack
				for len(pending) > 0 && pending[0].sequence <= ack {
					writeAcknowledged += len(pending[0].payload)
					pending = pending[1:]
				}
				select {
				case stream.activity <- struct{}{}:
				default:
				}
			}
			if flags == flagAck {
				if sequence != 0 || len(payload) != headerSize {
					terminalError = errors.New("netchan: malformed acknowledgement")
					return
				}
				continue
			}
			if sequence == 0 || flags == flagFinish && len(payload) != headerSize {
				terminalError = errors.New("netchan: malformed data packet")
				return
			}
			if sequence < incomingSequence {
				acknowledgement = encode(flagAck, 0, incomingSequence-1, nil)
				continue
			}
			if sequence-incomingSequence >= maximumWindow {
				acknowledgement = encode(flagAck, 0, incomingSequence-1, nil)
				continue
			}
			if _, exists := reordered[sequence]; !exists {
				reordered[sequence] = receivePacket{payload: append([]byte(nil), payload[headerSize:]...), finish: flags == flagFinish}
			}
		case <-ticker.C:
			if activeWrite != nil && !writeDeadline.IsZero() && !time.Now().Before(writeDeadline) {
				terminalError = os.ErrDeadlineExceeded
				return
			}
			if drain != nil && !time.Now().Before(drain.deadline) {
				drain.reply <- os.ErrDeadlineExceeded
				drain = nil
			}
		case <-stream.link.Done():
			return
		}
	}
}
