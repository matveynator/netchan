package netchan

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Public native channel tests
////////////////////////////////////////////////////////////////////////////////

func testTLSConfigurations(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{Organization: []string{"NetChan test"}},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature |
			x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	rootCertificates := x509.NewCertPool()
	if !rootCertificates.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("failed to trust test certificate")
	}

	serverTLS := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	clientTLS := &tls.Config{RootCAs: rootCertificates, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	return serverTLS, clientTLS
}

func TestChannelCapacityAppliesOnlyToSend(t *testing.T) {
	channel := newChannel[int](7)
	if capacity := cap(channel.Send); capacity != 7 {
		t.Fatalf("Send capacity = %d, want 7", capacity)
	}
	if capacity := cap(channel.Receive); capacity != 0 {
		t.Fatalf("Receive capacity = %d, want 0", capacity)
	}
	if _, err := normalizeConfig([]Config{{Capacity: -1}}); err == nil {
		t.Fatal("negative capacity was accepted")
	}
	if _, err := normalizeConfig([]Config{{}, {}}); err == nil {
		t.Fatal("multiple configurations were accepted")
	}
	if _, err := normalizeConfig(nil); err == nil {
		t.Fatal("missing TLS configuration was accepted")
	}
	var zero Channel[int]
	if err := zero.Deliver(1); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("zero Channel Deliver error = %v", err)
	}
	if err := zero.Close(); err != nil {
		t.Fatalf("zero Channel Close error = %v", err)
	}
}

func TestNativeSendHandsOwnershipToLocalBridge(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 0, 0)
	sent := make(chan struct{})
	go func() {
		client.Send <- 42
		close(sent)
	}()
	waitForClose(t, sent)
	if received := receiveWithTimeout(t, server.Receive); received != 42 {
		t.Fatalf("received = %d, want 42", received)
	}
}

func TestDeliverWaitsForRemoteReceive(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 0, 0)
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(73) }()
	assertStillWaiting(t, delivered)
	if received := receiveWithTimeout(t, server.Receive); received != 73 {
		t.Fatalf("received = %d, want 73", received)
	}
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverReturnsChannelClosedWhenPeerCloses(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 0, 0)
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(73) }()
	assertStillWaiting(t, delivered)

	serverClosed := make(chan error, 1)
	go func() { serverClosed <- server.Close() }()
	if err := receiveWithTimeout(t, delivered); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("Deliver error = %v", err)
	}
	if err := receiveWithTimeout(t, serverClosed); err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(91); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("Deliver after Done error = %v", err)
	}
}

func TestAbortCancelsPendingDelivery(t *testing.T) {
	client, _, _, _ := connectedChannels[int](t, 0, 0)
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(73) }()
	assertStillWaiting(t, delivered)
	if err := client.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithTimeout(t, delivered); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("Deliver after Abort error = %v", err)
	}
	waitForClose(t, client.Done)
	if err := client.Abort(); err != nil {
		t.Fatalf("second Abort error = %v", err)
	}
}

func TestNativeChannelWorksInBothDirectionsWithSelect(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 0, 0)
	select {
	case client.Send <- 73:
	case <-client.Done:
		t.Fatal("client closed before send")
	}
	select {
	case received := <-server.Receive:
		if received != 73 {
			t.Fatalf("server received = %d, want 73", received)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server receive timed out")
	}
	select {
	case server.Send <- 91:
	case <-server.Done:
		t.Fatal("server closed before send")
	}
	if received := receiveWithTimeout(t, client.Receive); received != 91 {
		t.Fatalf("client received = %d, want 91", received)
	}
}

func TestSendThenDeliverPreservesBufferedOrder(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 4, 0)
	client.Send <- 1
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(2) }()
	assertStillWaiting(t, delivered)
	if received := receiveWithTimeout(t, server.Receive); received != 1 {
		t.Fatalf("first value = %d, want 1", received)
	}
	assertStillWaiting(t, delivered)
	if received := receiveWithTimeout(t, server.Receive); received != 2 {
		t.Fatalf("second value = %d, want 2", received)
	}
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDrainsAcceptedValuesAndClosesLifecycleChannels(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 2, 0)
	client.Send <- 1
	client.Send <- 2
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	assertStillWaiting(t, closed)

	if received := receiveWithTimeout(t, server.Receive); received != 1 {
		t.Fatalf("first value = %d, want 1", received)
	}
	assertStillWaiting(t, closed)
	if received := receiveWithTimeout(t, server.Receive); received != 2 {
		t.Fatalf("second value = %d, want 2", received)
	}
	if err := receiveWithTimeout(t, closed); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, client.Done)
	waitForClose(t, client.Receive)
	waitForClose(t, client.Errors)
	waitForClose(t, server.Receive)
}

func TestNativeCloseSendDrainsAndClosesChannel(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 2, 0)
	client.Send <- 1
	client.Send <- 2
	close(client.Send)
	assertStillWaiting(t, client.Done)

	if received := receiveWithTimeout(t, server.Receive); received != 1 {
		t.Fatalf("first value = %d, want 1", received)
	}
	if received := receiveWithTimeout(t, server.Receive); received != 2 {
		t.Fatalf("second value = %d, want 2", received)
	}
	waitForClose(t, client.Done)
	waitForClose(t, client.Receive)
	waitForClose(t, client.Errors)
	waitForClose(t, server.Receive)
	waitForClose(t, server.Done)
	if err := client.Close(); err != nil {
		t.Fatalf("Close after native close = %v", err)
	}
}

type nestedTask struct {
	Name  string
	Reply chan<- string
	Done  <-chan struct{}
}

func TestDirectionalChannelsTravelInsideChannel(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	reply := make(chan string)
	done := make(chan struct{})
	delivered := make(chan error, 1)
	go func() {
		delivered <- client.Deliver(nestedTask{Name: "compile", Reply: reply, Done: done})
	}()
	task := receiveWithTimeout(t, server.Receive)
	if task.Name != "compile" || task.Reply == nil || task.Done == nil {
		t.Fatalf("task = %#v", task)
	}
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}

	replied := make(chan struct{})
	go func() {
		defer close(task.Reply)
		select {
		case task.Reply <- "complete":
		case <-task.Done:
		case <-server.Done:
		}
		close(replied)
	}()
	waitForClose(t, replied)
	if result := receiveWithTimeout(t, reply); result != "complete" {
		t.Fatalf("reply = %q, want complete", result)
	}
	waitForClose(t, reply)

	close(done)
	waitForClose(t, task.Done)
}

func TestTaskCancellationArrivesBeforeReply(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	reply := make(chan string)
	done := make(chan struct{})
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(nestedTask{Name: "cancel", Reply: reply, Done: done}) }()
	task := receiveWithTimeout(t, server.Receive)
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		defer close(task.Reply)
		select {
		case <-task.Done:
		case <-server.Done:
		}
	}()
	close(done)
	waitForClose(t, task.Done)
	waitForClose(t, handlerDone)
	waitForClose(t, reply)
}

func TestTaskCancellationSurvivesUntilParentReceive(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	reply := make(chan string)
	done := make(chan struct{})
	client.Send <- nestedTask{Name: "already cancelled", Reply: reply, Done: done}
	close(done)

	task := receiveWithTimeout(t, server.Receive)
	waitForClose(t, task.Done)
	close(task.Reply)
	waitForClose(t, reply)
}

func TestClosingNestedSendRightClosesOriginalChannel(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	reply := make(chan string)
	done := make(chan struct{})
	go func() { _ = client.Deliver(nestedTask{Name: "close", Reply: reply, Done: done}) }()
	task := receiveWithTimeout(t, server.Receive)
	close(task.Reply)
	waitForClose(t, reply)
	close(done)
}

func TestClosedOriginalReplyDoesNotPanicNetworkBridge(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	reply := make(chan string)
	done := make(chan struct{})
	go func() { _ = client.Deliver(nestedTask{Name: "abandoned", Reply: reply, Done: done}) }()
	task := receiveWithTimeout(t, server.Receive)
	close(reply)

	task.Reply <- "late"
	select {
	case err := <-client.Errors:
		if !errors.Is(err, ErrChannelClosed) {
			t.Fatalf("bridge error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed original reply was not reported")
	}
	close(done)
}

type repeatedReplyRights struct {
	First  chan<- string
	Second chan<- string
}

type bufferedStreamRight struct {
	Values <-chan int
}

type multipleStreamRights struct {
	Values <-chan int
}

func TestBufferedDirectionalCapabilityPreservesCapacityAndDrain(t *testing.T) {
	client, server, _, _ := connectedChannels[bufferedStreamRight](t, 0, 0)
	values := make(chan int, 32)
	for number := 0; number < cap(values); number++ {
		values <- number
	}
	close(values)

	client.Send <- bufferedStreamRight{Values: values}
	right := receiveWithTimeout(t, server.Receive)
	if capacity := cap(right.Values); capacity != 32 {
		t.Fatalf("nested channel capacity = %d, want 32", capacity)
	}
	for number := 0; number < 32; number++ {
		select {
		case received, open := <-right.Values:
			if !open {
				t.Fatalf("nested channel closed before value %d", number)
			}
			if received != number {
				t.Fatalf("value %d = %d", number, received)
			}
		case err := <-server.Errors:
			t.Fatalf("nested bridge failed before value %d: %v", number, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for nested value %d", number)
		}
	}
	waitForClose(t, right.Values)
}

func TestNestedCapabilityStaysDormantUntilParentReceive(t *testing.T) {
	client, server, _, _ := connectedChannels[bufferedStreamRight](t, 0, 0)
	values := make(chan int)
	client.Send <- bufferedStreamRight{Values: values}

	accepted := make(chan struct{})
	go func() {
		values <- 19
		close(accepted)
	}()
	assertStillWaiting(t, accepted)

	right := receiveWithTimeout(t, server.Receive)
	waitForClose(t, accepted)
	if received := receiveWithTimeout(t, right.Values); received != 19 {
		t.Fatalf("nested value = %d, want 19", received)
	}
	close(values)
	waitForClose(t, right.Values)
}

func TestRepeatedDirectionalCapabilityPreservesIdentity(t *testing.T) {
	client, server, _, _ := connectedChannels[repeatedReplyRights](t, 0, 0)
	reply := make(chan string)
	go func() { _ = client.Deliver(repeatedReplyRights{First: reply, Second: reply}) }()
	rights := receiveWithTimeout(t, server.Receive)
	if rights.First != rights.Second {
		t.Fatal("the same channel was decoded as two capabilities")
	}
	close(rights.First)
	waitForClose(t, reply)
}

func TestCapabilityCloseDoesNotWaitForLaterParentReceive(t *testing.T) {
	client, server, observed := connectedObservedChannels[multipleStreamRights](t, 0, 0)
	values := make(chan int)

	firstDelivered := make(chan error, 1)
	go func() { firstDelivered <- client.Deliver(multipleStreamRights{Values: values}) }()
	firstPrepared := waitForObservedFrame(t, observed, func(observation observedNetworkFrame) bool {
		return !observation.fromClient && observation.frame.Kind == frameChannelPrepared
	})
	first := receiveWithTimeout(t, server.Receive)
	if err := receiveWithTimeout(t, firstDelivered); err != nil {
		t.Fatal(err)
	}

	secondDelivered := make(chan error, 1)
	go func() { secondDelivered <- client.Deliver(multipleStreamRights{Values: values}) }()
	waitForObservedFrame(t, observed, func(observation observedNetworkFrame) bool {
		return !observation.fromClient && observation.frame.Kind == frameChannelPrepared && observation.frame.ChannelID == firstPrepared.ChannelID
	})

	close(values)
	waitForClose(t, first.Values)

	second := receiveWithTimeout(t, server.Receive)
	if err := receiveWithTimeout(t, secondDelivered); err != nil {
		t.Fatal(err)
	}
	if first.Values != second.Values {
		t.Fatal("a later parent replaced the closed capability proxy")
	}
	waitForClose(t, second.Values)
}

func TestPreparedParentDoesNotBlockAnotherCapability(t *testing.T) {
	client, server, observed := connectedObservedChannels[multipleStreamRights](t, 0, 0)
	values := make(chan int)

	firstDelivered := make(chan error, 1)
	go func() { firstDelivered <- client.Deliver(multipleStreamRights{Values: values}) }()
	firstPrepared := waitForObservedFrame(t, observed, func(observation observedNetworkFrame) bool {
		return !observation.fromClient && observation.frame.Kind == frameChannelPrepared
	})
	first := receiveWithTimeout(t, server.Receive)
	if err := receiveWithTimeout(t, firstDelivered); err != nil {
		t.Fatal(err)
	}

	secondDelivered := make(chan error, 1)
	go func() { secondDelivered <- client.Deliver(multipleStreamRights{Values: values}) }()
	waitForObservedFrame(t, observed, func(observation observedNetworkFrame) bool {
		return !observation.fromClient && observation.frame.Kind == frameChannelPrepared && observation.frame.ChannelID == firstPrepared.ChannelID
	})

	valueAccepted := make(chan struct{})
	go func() {
		values <- 42
		close(valueAccepted)
	}()
	waitForClose(t, valueAccepted)
	if received := receiveWithTimeout(t, first.Values); received != 42 {
		t.Fatalf("stream value = %d, want 42", received)
	}

	second := receiveWithTimeout(t, server.Receive)
	if err := receiveWithTimeout(t, secondDelivered); err != nil {
		t.Fatal(err)
	}
	if first.Values != second.Values {
		t.Fatal("the repeated stream capability changed identity")
	}
	close(values)
	waitForClose(t, first.Values)
}

func TestSequentialTaskCapabilitiesAreReclaimed(t *testing.T) {
	client, server, _, _ := connectedChannels[nestedTask](t, 0, 0)
	for index := 0; index < maximumSessionCapabilities+32; index++ {
		reply := make(chan string)
		done := make(chan struct{})
		delivered := make(chan error, 1)
		go func() {
			delivered <- client.Deliver(nestedTask{Name: fmt.Sprint(index), Reply: reply, Done: done})
		}()

		task := receiveWithTimeout(t, server.Receive)
		if err := receiveWithTimeout(t, delivered); err != nil {
			t.Fatalf("task %d delivery: %v", index, err)
		}
		replied := make(chan struct{})
		go func() {
			task.Reply <- task.Name
			close(task.Reply)
			close(replied)
		}()
		if result := receiveWithTimeout(t, reply); result != task.Name {
			t.Fatalf("task %d result = %q, want %q", index, result, task.Name)
		}
		waitForClose(t, replied)
		waitForClose(t, reply)
		close(done)
		waitForClose(t, task.Done)
	}
}

func TestRootSchemaMismatchRejectsOpen(t *testing.T) {
	serverNode, _, clientSession, _ := connectedTestSessions(t)
	accepted := make(chan *Channel[int], 1)
	if err := publishRoot(serverNode, accepted, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRoot[string](clientSession, 0); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("open error = %v", err)
	}
}

func TestPeerCapacitiesAreIndependent(t *testing.T) {
	client, server, _, _ := connectedChannels[int](t, 3, 5)
	if cap(client.Send) != 3 || cap(server.Send) != 5 {
		t.Fatalf("capacities = client %d, server %d", cap(client.Send), cap(server.Send))
	}
	if cap(client.Receive) != 0 || cap(server.Receive) != 0 {
		t.Fatal("Receive must remain unbuffered")
	}
}

func TestDeliverSurvivesPhysicalReconnectAndDeliversOnce(t *testing.T) {
	client, server, clientSession, serverSession := connectedChannels[int](t, 0, 0)
	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(17) }()
	assertStillWaiting(t, delivered)

	disconnectSession(t, clientSession)
	clientConnection, serverConnection := net.Pipe()
	if attached := serverSession.attach(serverConnection); attached.err != nil {
		t.Fatal(attached.err)
	}
	if attached := clientSession.attach(clientConnection); attached.err != nil {
		t.Fatal(attached.err)
	}

	if received := receiveWithTimeout(t, server.Receive); received != 17 {
		t.Fatalf("receive after reconnect = %d, want 17", received)
	}
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-server.Receive:
		t.Fatalf("duplicate value delivered: %d", value)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestFullDeliveryWindowDoesNotBlockSessionAttach(t *testing.T) {
	unhandled := make(chan sessionUnhandledFrame)
	session := newSession("window-test", unhandled, 0, make(chan struct{}), 0)
	t.Cleanup(func() { _ = session.Close() })
	for index := 0; index < maximumPendingFrames; index++ {
		err := session.sendData(networkFrame{
			Kind: frameChannelValue, ChannelID: "channel",
			TransferID: fmt.Sprintf("transfer-%d", index), Payload: []byte{1},
		})
		if err != nil {
			t.Fatalf("fill delivery window %d: %v", index, err)
		}
	}

	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	attached := make(chan sessionAttachResult, 1)
	go func() { attached <- session.attach(local) }()
	select {
	case result := <-attached:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("session attach was blocked by a full delivery window")
	}
}

func TestTerminalCapabilityForgetIsRescheduledAfterLateReservation(t *testing.T) {
	session := newSession("forget-reschedule", make(chan sessionUnhandledFrame), 0, make(chan struct{}), 0)
	for index := 0; index < maximumPendingFrames; index++ {
		if err := session.send(networkFrame{Kind: frameChannelClosed, ChannelID: fmt.Sprintf("pending-%d", index)}); err != nil {
			t.Fatalf("fill pending window %d: %v", index, err)
		}
	}

	source := make(chan int)
	right := (<-chan int)(source)
	rightValue := reflect.ValueOf(right)
	identity := channelCapabilityIdentity{
		pointer: rightValue.Pointer(), direction: channelReceiveOnly, schema: schemaForType(rightValue.Type()),
	}
	identifier, err := randomIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := session.reserveExportCapability(identity, rightValue, identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !session.commitExportCapability(first) {
		t.Fatal("first capability reservation did not activate")
	}
	session.unregisterChannel(identifier)
	second, _, err := session.reserveExportCapability(identity, rightValue, "unused-identifier")
	if err != nil {
		t.Fatal(err)
	}

	local, remote := net.Pipe()
	peerDone := make(chan struct{})
	forgotten := make(chan struct{}, 1)
	go func() {
		defer close(peerDone)
		decoder := newFrameDecoder(remote)
		encoder := newFrameEncoder(remote)
		for {
			frame, err := decoder.decode()
			if err != nil {
				return
			}
			if frame.Kind == frameChannelForgotten && frame.ChannelID == identifier {
				select {
				case forgotten <- struct{}{}:
				default:
				}
			}
			if frame.Sequence > 0 {
				if err := encoder.encode(acknowledgementFrame(session.id, frame.Sequence)); err != nil {
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		_ = remote.Close()
		_ = session.Close()
		<-peerDone
	})
	if attached := session.attach(local); attached.err != nil {
		t.Fatal(attached.err)
	}
	if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: "forget-barrier"}); err != nil {
		t.Fatal(err)
	}
	session.cancelExportCapability(second)
	receiveWithTimeout(t, forgotten)
}

func TestReconnectReplayIsPacedBeyondPhysicalQueueCapacity(t *testing.T) {
	ownerDone := make(chan struct{})
	session := newSession("paced-replay", make(chan sessionUnhandledFrame), 0, ownerDone, 0)
	t.Cleanup(func() {
		close(ownerDone)
		_ = session.Close()
	})
	local, remote := net.Pipe()
	if attached := session.attach(local); attached.err != nil {
		t.Fatal(attached.err)
	}
	connectedEvent := receiveWithTimeout(t, session.Events())
	if !connectedEvent.Connected {
		t.Fatalf("first session event = %#v", connectedEvent)
	}
	peerStopped := make(chan struct{})
	go func() {
		defer close(peerStopped)
		decoder := newFrameDecoder(remote)
		encoder := newFrameEncoder(remote)
		for {
			frame, err := decoder.decode()
			if err != nil {
				return
			}
			if frame.Sequence == 0 {
				continue
			}
			if err := encoder.encode(acknowledgementFrame(session.id, frame.Sequence)); err != nil {
				return
			}
		}
	}()

	for index := 0; index < maximumPendingFrames; index++ {
		if err := session.sendData(networkFrame{
			Kind: frameChannelValue, ChannelID: "application",
			TransferID: fmt.Sprintf("application-%d", index), Payload: []byte{1},
		}); err != nil {
			t.Fatalf("application frame %d: %v", index, err)
		}
	}
	if err := session.sendConfirmed(networkFrame{Kind: frameChannelClosed, ChannelID: "transport-barrier"}); err != nil {
		t.Fatal(err)
	}
	disconnectSession(t, session)
	waitForClose(t, peerStopped)
	for {
		event := receiveWithTimeout(t, session.Events())
		if !event.Connected {
			break
		}
	}

	for index := 0; index < maximumPendingFrames; index++ {
		if err := session.send(networkFrame{Kind: frameChannelClosed, ChannelID: fmt.Sprintf("control-%d", index)}); err != nil {
			t.Fatalf("control frame %d: %v", index, err)
		}
	}
	secondLocal, secondRemote := net.Pipe()
	attached := make(chan sessionAttachResult, 1)
	go func() { attached <- session.attach(secondLocal) }()
	select {
	case result := <-attached:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session attach blocked while replay exceeded the physical queue")
	}
	_ = secondRemote.Close()
}

func TestMalformedRootDirectionDoesNotDeadlockActors(t *testing.T) {
	serverNode, _, clientSession, _ := connectedTestSessions(t)
	accepted := make(chan *Channel[int], 1)
	if err := publishRoot(serverNode, accepted, 0); err != nil {
		t.Fatal(err)
	}
	identifier, err := randomIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan networkFrame, channelFrameBufferSize)
	if err := clientSession.registerChannel(identifier, incoming); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientSession.unregisterChannel(identifier) })
	if err := clientSession.send(networkFrame{
		Kind: frameOpenChannel, ChannelID: identifier, Name: rootChannelName,
		Direction: channelSendOnly, Schema: schemaForType(reflect.TypeOf(int(0))),
	}); err != nil {
		t.Fatal(err)
	}
	frame := receiveWithTimeout(t, incoming)
	if frame.Kind != frameChannelRevoked || !errors.Is(errorFromNetworkFrame(frame), ErrSchemaMismatch) {
		t.Fatalf("malformed root response = %#v", frame)
	}
}

func TestPhysicalFailureUnblocksAllWriters(t *testing.T) {
	events := make(chan physicalConnectionEvent, 4)
	sessionDone := make(chan struct{})
	local, remote := net.Pipe()
	physical := runPhysicalConnection(local, events, sessionDone, nil)
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, physical.done)
	if err := physical.sendAndWait(networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "physical"}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("send after physical failure = %v", err)
	}
	close(sessionDone)
}

func TestPhysicalWriteHonorsOneAbsoluteDeadline(t *testing.T) {
	events := make(chan physicalConnectionEvent, 4)
	sessionDone := make(chan struct{})
	local, remote := net.Pipe()
	physical := runPhysicalConnection(local, events, sessionDone, nil)
	t.Cleanup(func() {
		_ = remote.Close()
		close(sessionDone)
	})
	deadline := time.Now().Add(30 * time.Millisecond)
	err := physical.sendAndWaitUntil(networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "deadline"}, deadline)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write deadline error = %v", err)
	}
	waitForClose(t, physical.done)
}

func TestFlushSessionClosePreservesTerminalFrameOrder(t *testing.T) {
	events := make(chan physicalConnectionEvent, 4)
	sessionDone := make(chan struct{})
	local, remote := net.Pipe()
	physical := runPhysicalConnection(local, events, sessionDone, nil)
	t.Cleanup(func() {
		_ = remote.Close()
		close(sessionDone)
	})
	received := make(chan []networkFrame, 1)
	go func() {
		decoder := newFrameDecoder(remote)
		frames := make([]networkFrame, 0, 3)
		for len(frames) < 3 {
			frame, err := decoder.decode()
			if err != nil {
				return
			}
			frames = append(frames, frame)
		}
		received <- frames
	}()

	queued := []networkFrame{
		{Version: protocolVersion, Kind: frameChannelClosed, SessionID: "closing", Sequence: 1, ChannelID: "first"},
		{Version: protocolVersion, Kind: frameChannelForgotten, SessionID: "closing", Sequence: 2, ChannelID: "second"},
	}
	flushSessionClose(physical, queued, "closing")
	frames := receiveWithTimeout(t, received)
	if frames[0].ChannelID != "first" || frames[1].ChannelID != "second" || frames[2].Kind != frameSessionClosed {
		t.Fatalf("shutdown wire order = %#v", frames)
	}
}

func TestServerSessionExpiresWithoutRootChannel(t *testing.T) {
	session := newSession("root-admission", make(chan sessionUnhandledFrame), 0, make(chan struct{}), 30*time.Millisecond)
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = remote.Close()
		_ = session.Close()
	})
	if attached := session.attach(local); attached.err != nil {
		t.Fatal(attached.err)
	}
	if err := receiveWithTimeout(t, session.Errors()); !errors.Is(err, ErrPayloadLimit) {
		t.Fatalf("root admission error = %v", err)
	}
	waitForClose(t, session.Done())
}

func TestRejectedRootOpenDoesNotCancelAdmissionTimeout(t *testing.T) {
	unhandled := make(chan sessionUnhandledFrame)
	session := newSession("rejected-root", unhandled, 0, make(chan struct{}), 30*time.Millisecond)
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = remote.Close()
		_ = session.Close()
	})
	go func() {
		request := <-unhandled
		request.reply <- sessionUnhandledResult{err: ErrSchemaMismatch}
	}()
	if attached := session.attach(local); attached.err != nil {
		t.Fatal(attached.err)
	}
	open := networkFrame{
		Version: protocolVersion, Kind: frameOpenChannel, SessionID: session.id, Sequence: 1,
		ChannelID: "root", Name: "missing", Direction: channelBidirectional, Schema: schemaForType(reflect.TypeOf(int(0))),
	}
	if err := newFrameEncoder(remote).encode(open); err != nil {
		t.Fatal(err)
	}
	response := receiveNetworkFrameKind(t, remote, frameChannelRevoked)
	if response.Kind != frameChannelRevoked {
		t.Fatalf("rejected root response = %#v", response)
	}
	if err := receiveWithTimeout(t, session.Errors()); !errors.Is(err, ErrPayloadLimit) {
		t.Fatalf("root admission error = %v", err)
	}
	waitForClose(t, session.Done())
}

func TestAcceptedRootOpenCancelsAdmissionTimeout(t *testing.T) {
	unhandled := make(chan sessionUnhandledFrame)
	session := newSession("accepted-root", unhandled, 0, make(chan struct{}), 30*time.Millisecond)
	local, remote := net.Pipe()
	t.Cleanup(func() {
		_ = remote.Close()
		_ = session.Close()
	})
	go func() {
		request := <-unhandled
		request.reply <- sessionUnhandledResult{incoming: make(chan networkFrame, 1)}
	}()
	if attached := session.attach(local); attached.err != nil {
		t.Fatal(attached.err)
	}
	open := networkFrame{
		Version: protocolVersion, Kind: frameOpenChannel, SessionID: session.id, Sequence: 1,
		ChannelID: "root", Name: rootChannelName, Direction: channelBidirectional, Schema: schemaForType(reflect.TypeOf(int(0))),
	}
	if err := newFrameEncoder(remote).encode(open); err != nil {
		t.Fatal(err)
	}
	response := receiveNetworkFrameKind(t, remote, frameChannelOpened)
	if response.Kind != frameChannelOpened {
		t.Fatalf("accepted root response = %#v", response)
	}
	select {
	case <-session.Done():
		t.Fatal("accepted root session expired")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestPublicListenAndDial(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow a local TCP listener")
	}
	serverTLS, clientTLS := testTLSConfigurations(t)
	listener, err := Listen[string]("127.0.0.1:0", Config{TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := Dial[string](listener.Address, Config{TLS: clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Abort() })
	server := receiveWithTimeout(t, listener.Channels)

	sent := make(chan error, 1)
	go func() { sent <- client.Deliver("hello") }()
	if message := receiveWithTimeout(t, server.Receive); message != "hello" {
		t.Fatalf("server Receive = %q", message)
	}
	if err := receiveWithTimeout(t, sent); err != nil {
		t.Fatal(err)
	}
}

func TestPublicConfigAndListenerLifecycle(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow a local TCP listener")
	}
	serverTLS, clientTLS := testTLSConfigurations(t)
	listener, err := Listen[int]("127.0.0.1:0", Config{Capacity: 5, TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := Dial[int](listener.Address, Config{Capacity: 3, TLS: clientTLS})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Abort() })
	server := receiveWithTimeout(t, listener.Channels)
	if cap(client.Send) != 3 || cap(server.Send) != 5 {
		t.Fatalf("configured capacities = client %d, server %d", cap(client.Send), cap(server.Send))
	}

	delivered := make(chan error, 1)
	go func() { delivered <- client.Deliver(41) }()
	if received := receiveWithTimeout(t, server.Receive); received != 41 {
		t.Fatalf("server Receive = %d, want 41", received)
	}
	if err := receiveWithTimeout(t, delivered); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, server.Done)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, listener.Done)
	waitForClose(t, listener.Channels)
	waitForClose(t, listener.Errors)
}

func TestPublicClientsRemainIndependent(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow a local TCP listener")
	}
	serverTLS, clientTLS := testTLSConfigurations(t)
	listener, err := Listen[string]("127.0.0.1:0", Config{TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	firstClient, err := Dial[string](listener.Address, Config{TLS: clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstClient.Abort() })
	firstServer := receiveWithTimeout(t, listener.Channels)
	secondClient, err := Dial[string](listener.Address, Config{TLS: clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondClient.Abort() })
	secondServer := receiveWithTimeout(t, listener.Channels)

	firstDelivered := make(chan error, 1)
	secondDelivered := make(chan error, 1)
	go func() { firstDelivered <- firstClient.Deliver("first") }()
	go func() { secondDelivered <- secondClient.Deliver("second") }()
	if received := receiveWithTimeout(t, firstServer.Receive); received != "first" {
		t.Fatalf("first server received %q", received)
	}
	if received := receiveWithTimeout(t, secondServer.Receive); received != "second" {
		t.Fatalf("second server received %q", received)
	}
	if err := receiveWithTimeout(t, firstDelivered); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithTimeout(t, secondDelivered); err != nil {
		t.Fatal(err)
	}

	if err := firstClient.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, firstServer.Done)
	stillDelivered := make(chan error, 1)
	go func() { stillDelivered <- secondClient.Deliver("still connected") }()
	if received := receiveWithTimeout(t, secondServer.Receive); received != "still connected" {
		t.Fatalf("second server after first close received %q", received)
	}
	if err := receiveWithTimeout(t, stillDelivered); err != nil {
		t.Fatal(err)
	}
}

func TestPublicListenerCloseTerminatesActiveChannel(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow a local TCP listener")
	}
	serverTLS, clientTLS := testTLSConfigurations(t)
	listener, err := Listen[int]("127.0.0.1:0", Config{TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial[int](listener.Address, Config{TLS: clientTLS})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Abort() })
	server := receiveWithTimeout(t, listener.Channels)

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, listener.Done)
	waitForClose(t, server.Done)
	waitForClose(t, client.Done)
}

func TestPublicListenerCloseInterruptsIncompleteHandshake(t *testing.T) {
	if os.Getenv("NETCHAN_NETWORK_TEST") == "" {
		t.Skip("set NETCHAN_NETWORK_TEST=1 to allow a local TCP listener")
	}
	serverTLS, _ := testTLSConfigurations(t)
	listener, err := Listen[int]("127.0.0.1:0", Config{TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	rawConnection, err := net.Dial("tcp", listener.Address)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawConnection.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for len(listener.node.handshakeSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(listener.node.handshakeSlots) == 0 {
		_ = listener.Close()
		t.Fatal("listener did not begin the incomplete handshake")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	waitForClose(t, listener.Done)
	if err := rawConnection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var oneByte [1]byte
	_, readErr := rawConnection.Read(oneByte[:])
	if readErr == nil {
		t.Fatal("incomplete handshake remained open after listener close")
	}
	var networkError net.Error
	if errors.Is(readErr, os.ErrDeadlineExceeded) || errors.As(readErr, &networkError) && networkError.Timeout() {
		t.Fatalf("listener close did not interrupt the incomplete handshake: %v", readErr)
	}
}

func connectedChannels[T any](t *testing.T, clientCapacity, serverCapacity int) (*Channel[T], *Channel[T], *session, *session) {
	t.Helper()
	serverNode, _, clientSession, serverSession := connectedTestSessions(t)
	accepted := make(chan *Channel[T], 1)
	if err := publishRoot(serverNode, accepted, serverCapacity); err != nil {
		t.Fatal(err)
	}
	clientChannel, _, err := openRoot[T](clientSession, clientCapacity)
	if err != nil {
		t.Fatal(err)
	}
	serverChannel := receiveWithTimeout(t, accepted)
	return clientChannel, serverChannel, clientSession, serverSession
}

type observedNetworkFrame struct {
	fromClient bool
	frame      networkFrame
}

func connectedObservedChannels[T any](t *testing.T, clientCapacity, serverCapacity int) (*Channel[T], *Channel[T], <-chan observedNetworkFrame) {
	t.Helper()
	observed := make(chan observedNetworkFrame, 256)
	serverNode, _, clientSession, _ := connectedTestSessionsWithObservation(t, observed)
	accepted := make(chan *Channel[T], 1)
	if err := publishRoot(serverNode, accepted, serverCapacity); err != nil {
		t.Fatal(err)
	}
	clientChannel, _, err := openRoot[T](clientSession, clientCapacity)
	if err != nil {
		t.Fatal(err)
	}
	serverChannel := receiveWithTimeout(t, accepted)
	return clientChannel, serverChannel, observed
}

func connectedTestSessions(t *testing.T) (*node, *node, *session, *session) {
	return connectedTestSessionsWithObservation(t, nil)
}

func connectedTestSessionsWithObservation(t *testing.T, observed chan<- observedNetworkFrame) (*node, *node, *session, *session) {
	t.Helper()
	serverTLS, clientTLS := testTLSConfigurations(t)
	serverNode, err := newNode(withTLSConfigs(serverTLS, clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	clientNode, err := newNode(withTLSConfigs(serverTLS, clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := randomIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	clientSession := newSession(identifier, clientNode.unhandled, 0, clientNode.stopping, 0)
	if err := clientNode.registerSession(clientSession); err != nil {
		t.Fatal(err)
	}
	serverSessionReply := make(chan *session, 1)
	serverNode.commands <- nodeFindSessionCommand{identifier: identifier, create: true, reply: serverSessionReply}
	serverSession := <-serverSessionReply
	clientConnection, serverConnection := net.Pipe()
	if observed != nil {
		clientConnection, serverConnection = observedConnectionPair(t, observed)
	}
	if attached := serverSession.attach(serverConnection); attached.err != nil {
		t.Fatal(attached.err)
	}
	if attached := clientSession.attach(clientConnection); attached.err != nil {
		t.Fatal(attached.err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = clientNode.Close()
		_ = serverNode.Close()
	})
	return serverNode, clientNode, clientSession, serverSession
}

func observedConnectionPair(t *testing.T, observed chan<- observedNetworkFrame) (net.Conn, net.Conn) {
	t.Helper()
	clientConnection, clientRelay := net.Pipe()
	serverRelay, serverConnection := net.Pipe()
	t.Cleanup(func() {
		_ = clientRelay.Close()
		_ = serverRelay.Close()
	})
	go relayNetworkFrames(clientRelay, serverRelay, true, observed)
	go relayNetworkFrames(serverRelay, clientRelay, false, observed)
	return clientConnection, serverConnection
}

func relayNetworkFrames(source, destination net.Conn, fromClient bool, observed chan<- observedNetworkFrame) {
	defer source.Close()
	defer destination.Close()
	decoder := newFrameDecoder(source)
	encoder := newFrameEncoder(destination)
	for {
		frame, err := decoder.decode()
		if err != nil {
			return
		}
		observed <- observedNetworkFrame{fromClient: fromClient, frame: frame}
		if err := encoder.encode(frame); err != nil {
			return
		}
	}
}

func disconnectSession(t *testing.T, session *session) {
	t.Helper()
	reply := make(chan struct{})
	session.commands <- sessionDisconnectCommand{reply: reply}
	<-reply
}

func assertStillWaiting[T any](t *testing.T, result <-chan T) {
	t.Helper()
	select {
	case <-result:
		t.Fatal("operation completed before the peer received the value")
	case <-time.After(30 * time.Millisecond):
	}
}

func receiveWithTimeout[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value, open := <-channel:
		if !open {
			var zero T
			t.Fatal("channel closed before a value was received")
			return zero
		}
		return value
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatal("timed out waiting for channel value")
		return zero
	}
}

func waitForObservedFrame(t *testing.T, observed <-chan observedNetworkFrame, matches func(observedNetworkFrame) bool) networkFrame {
	t.Helper()
	for {
		select {
		case observation := <-observed:
			if matches(observation) {
				return observation.frame
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for an observed network frame")
			return networkFrame{}
		}
	}
}

func receiveNetworkFrameKind(t *testing.T, connection net.Conn, kind frameKind) networkFrame {
	t.Helper()
	type decodeResult struct {
		frame networkFrame
		err   error
	}
	result := make(chan decodeResult, 1)
	go func() {
		decoder := newFrameDecoder(connection)
		for {
			frame, err := decoder.decode()
			if err != nil || frame.Kind == kind {
				result <- decodeResult{frame: frame, err: err}
				return
			}
		}
	}()
	select {
	case decoded := <-result:
		if decoded.err != nil {
			t.Fatal(decoded.err)
		}
		return decoded.frame
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for network frame")
		return networkFrame{}
	}
}

func waitForClose[T any](t *testing.T, channel <-chan T) {
	t.Helper()
	select {
	case _, open := <-channel:
		if open {
			t.Fatal("received a value while waiting for channel closure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for channel closure")
	}
}

////////////////////////////////////////////////////////////////////////////////
// END: Public native channel tests
////////////////////////////////////////////////////////////////////////////////

////////////////////////////////////////////////////////////////////////////////
// BEGIN: Version 2 binary protocol tests
////////////////////////////////////////////////////////////////////////////////

type binaryProtocolFixture struct {
	Enabled bool
	Count   int64
	Name    string
	Bytes   []byte
	Labels  map[string]uint32
}

type explicitBinaryValue struct {
	number uint64
}

type transactionalCapabilityPayload struct {
	Reply  chan<- int
	Marker uint64
}

type capabilityLimitPayload struct {
	Replies [maximumValueCapabilities + 1]chan<- int
}

type oversizedArrayType [maximumCollectionLength + 1]byte

func TestProtocolVersionIsSecondPublishedVersion(t *testing.T) {
	if protocolVersion != 2 {
		t.Fatalf("protocol version = %d, want 2", protocolVersion)
	}

	for _, unsupportedVersion := range []uint16{1, 3, 4, 5} {
		frame := networkFrame{Version: unsupportedVersion, Kind: frameHeartbeat, SessionID: "session"}
		if err := validateNetworkFrame(frame); err == nil {
			t.Fatalf("protocol version %d was accepted", unsupportedVersion)
		}
	}
}

func (value explicitBinaryValue) MarshalBinary() ([]byte, error) {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value.number)
	return encoded, nil
}

func (value *explicitBinaryValue) UnmarshalBinary(encoded []byte) error {
	if len(encoded) != 8 {
		return errors.New("invalid explicit binary value")
	}
	value.number = binary.BigEndian.Uint64(encoded)
	return nil
}

func TestBinaryNetworkFramesRoundTrip(t *testing.T) {
	frames := []networkFrame{
		{Version: protocolVersion, Kind: frameHello, SessionID: "session", Sequence: 1},
		{Version: protocolVersion, Kind: frameHelloResume, SessionID: "session"},
		{Version: protocolVersion, Kind: frameHelloAccepted, SessionID: "session"},
		{Version: protocolVersion, Kind: frameSessionUnknown, SessionID: "session"},
		{Version: protocolVersion, Kind: frameSessionRejected, SessionID: "session"},
		{Version: protocolVersion, Kind: frameAcknowledgement, SessionID: "session", Ack: 7},
		{Version: protocolVersion, Kind: frameOpenChannel, SessionID: "session", Sequence: 2, ChannelID: "channel", Name: rootChannelName, Direction: channelBidirectional, Schema: schemaForType(reflect.TypeOf(int(0)))},
		{Version: protocolVersion, Kind: frameChannelOpened, SessionID: "session", Sequence: 3, ChannelID: "channel"},
		{Version: protocolVersion, Kind: frameChannelValue, SessionID: "session", Sequence: 4, ChannelID: "channel", TransferID: "transfer", Payload: []byte{1, 2, 3}},
		{Version: protocolVersion, Kind: frameChannelPrepared, SessionID: "session", Sequence: 5, ChannelID: "channel", TransferID: "transfer"},
		{Version: protocolVersion, Kind: frameChannelDelivered, SessionID: "session", Sequence: 4, ChannelID: "channel", TransferID: "transfer"},
		{Version: protocolVersion, Kind: frameChannelReleased, SessionID: "session", Sequence: 5, ChannelID: "channel", TransferID: "transfer"},
		{Version: protocolVersion, Kind: frameChannelClosed, SessionID: "session", Sequence: 6, ChannelID: "channel"},
		{Version: protocolVersion, Kind: frameChannelForgotten, SessionID: "session", Sequence: 7, ChannelID: "channel"},
		{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "session"},
	}
	for _, frame := range frames {
		encoded, err := encodeNetworkFrame(frame)
		if err != nil {
			t.Fatalf("encode kind %d: %v", frame.Kind, err)
		}
		decoded, err := decodeNetworkFrame(encoded)
		if err != nil {
			t.Fatalf("decode kind %d: %v", frame.Kind, err)
		}
		if !reflect.DeepEqual(decoded, frame) {
			t.Fatalf("frame kind %d round trip = %#v, want %#v", frame.Kind, decoded, frame)
		}
	}
}

func TestUnknownResumeDoesNotCreateServerSession(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigurations(t)
	node, err := newNode(withTLSConfigs(serverTLS, clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	serverConnection, peerConnection := net.Pipe()
	t.Cleanup(func() { _ = peerConnection.Close() })
	go node.acceptConnection(serverConnection)

	hello := networkFrame{Version: protocolVersion, Kind: frameHelloResume, SessionID: "missing-session"}
	if err := newFrameEncoder(peerConnection).encode(hello); err != nil {
		t.Fatal(err)
	}
	response, err := newHandshakeFrameDecoder(peerConnection).decode()
	if err != nil {
		t.Fatal(err)
	}
	if response.Kind != frameSessionUnknown || response.SessionID != hello.SessionID {
		t.Fatalf("unknown resume response = %#v", response)
	}

	reply := make(chan *session, 1)
	node.commands <- nodeFindSessionCommand{identifier: hello.SessionID, reply: reply}
	if session := <-reply; session != nil {
		t.Fatal("unknown resume created a server session")
	}
}

func TestHandshakeResponseDistinguishesRejectionAndExpiration(t *testing.T) {
	unknown := networkFrame{Version: protocolVersion, Kind: frameSessionUnknown, SessionID: "session"}
	if err := validateHandshakeResponse(unknown, "session", true); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("resume rejection error = %v", err)
	}
	if err := validateHandshakeResponse(unknown, "session", false); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("fresh unknown-session error = %v", err)
	}

	rejected := networkFrame{Version: protocolVersion, Kind: frameSessionRejected, SessionID: "session"}
	if err := validateHandshakeResponse(rejected, "session", false); !errors.Is(err, ErrSessionRejected) {
		t.Fatalf("fresh admission error = %v", err)
	}
	if err := validateHandshakeResponse(rejected, "session", true); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("resume admission-rejection error = %v", err)
	}
}

func TestHandshakeDecoderRejectsLargeFrameBeforeAllocation(t *testing.T) {
	var encodedSize [4]byte
	binary.BigEndian.PutUint32(encodedSize[:], maximumHandshakeFrameSize+1)
	if _, err := newHandshakeFrameDecoder(bytes.NewReader(encodedSize[:])).decode(); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("oversized handshake error = %v", err)
	}
}

func TestBinaryPayloadRoundTrip(t *testing.T) {
	original := binaryProtocolFixture{Enabled: true, Count: -91, Name: "binary", Bytes: []byte{0, 1, 2}, Labels: map[string]uint32{"one": 1}}
	payload, err := encodeChannelValue(nil, reflect.ValueOf(original))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeChannelValue(nil, reflect.TypeOf(original), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Interface(), original) {
		t.Fatalf("decoded payload = %#v", decoded.Interface())
	}
}

func TestExplicitBinaryCodecSupportsPrivateState(t *testing.T) {
	original := explicitBinaryValue{number: 99}
	payload, err := encodeChannelValue(nil, reflect.ValueOf(original))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeChannelValue(nil, reflect.TypeOf(original), payload)
	if err != nil || decoded.Interface().(explicitBinaryValue).number != 99 {
		t.Fatalf("decoded explicit value = %#v, %v", decoded, err)
	}
}

func TestMalformedParentRollsBackImportedCapability(t *testing.T) {
	_, _, clientSession, serverSession := connectedTestSessions(t)
	reply := make(chan int)
	original := transactionalCapabilityPayload{Reply: reply, Marker: 99}
	prepared, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(original))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(original), prepared.payload[:len(prepared.payload)-1]); err == nil {
		t.Fatal("truncated parent value was decoded")
	}
	decoded, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(original), prepared.payload)
	if err != nil {
		t.Fatal(err)
	}
	prepared.activate()
	remote := decoded.Interface().(transactionalCapabilityPayload)
	remote.Reply <- 7
	if received := receiveWithTimeout(t, reply); received != 7 {
		t.Fatalf("reply = %d, want 7", received)
	}
	close(remote.Reply)
	waitForClose(t, reply)
}

func TestCancelledEncoderReservationDoesNotRemoveConcurrentCapability(t *testing.T) {
	_, _, clientSession, serverSession := connectedTestSessions(t)
	reply := make(chan int)
	message := transactionalCapabilityPayload{Reply: reply, Marker: 7}
	first, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(message))
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(message))
	if err != nil {
		first.cancel()
		t.Fatal(err)
	}
	first.cancel()

	decoded, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(message), second.payload)
	if err != nil {
		second.cancel()
		t.Fatal(err)
	}
	second.activate()
	remote := decoded.Interface().(transactionalCapabilityPayload)
	remote.Reply <- 11
	if received := receiveWithTimeout(t, reply); received != 11 {
		t.Fatalf("reply = %d, want 11", received)
	}
	close(remote.Reply)
	waitForClose(t, reply)
}

func TestCancelledDecoderReservationDoesNotRemoveCommittedCapability(t *testing.T) {
	_, _, clientSession, serverSession := connectedTestSessions(t)
	reply := make(chan int)
	message := transactionalCapabilityPayload{Reply: reply, Marker: 9}
	prepared, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(message))
	if err != nil {
		t.Fatal(err)
	}

	deferred := binaryValueDecoder{
		session: serverSession,
		reader:  wireReader{bytes: prepared.payload},
		proxies: make(map[string]decodedChannelCapability),
	}
	deferredValue := reflect.New(reflect.TypeOf(message)).Elem()
	deferred.decode(deferredValue, 0)
	if err := deferred.reader.finish(); err != nil {
		prepared.cancel()
		t.Fatal(err)
	}

	decoded, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(message), prepared.payload)
	if err != nil {
		deferred.rollbackImportedCapabilities()
		prepared.cancel()
		t.Fatal(err)
	}
	deferred.rollbackImportedCapabilities()
	prepared.activate()
	remote := decoded.Interface().(transactionalCapabilityPayload)
	remote.Reply <- 13
	if received := receiveWithTimeout(t, reply); received != 13 {
		t.Fatalf("reply = %d, want 13", received)
	}
	close(remote.Reply)
	waitForClose(t, reply)
}

func TestCapabilityCloseDoesNotWaitForReservedParent(t *testing.T) {
	_, _, clientSession, serverSession := connectedTestSessions(t)
	reply := make(chan int)
	message := transactionalCapabilityPayload{Reply: reply, Marker: 21}
	first, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(message))
	if err != nil {
		t.Fatal(err)
	}
	firstDecoded, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(message), first.payload)
	if err != nil {
		first.cancel()
		t.Fatal(err)
	}
	first.activate()
	firstRemote := firstDecoded.Interface().(transactionalCapabilityPayload)

	second, err := prepareValidatedChannelValue(clientSession, reflect.ValueOf(message))
	if err != nil {
		t.Fatal(err)
	}
	close(firstRemote.Reply)
	waitForClose(t, reply)

	secondDecoded, err := decodeValidatedChannelValue(serverSession, reflect.TypeOf(message), second.payload)
	if err != nil {
		second.cancel()
		t.Fatal(err)
	}
	secondRemote := secondDecoded.Interface().(transactionalCapabilityPayload)
	if reflect.ValueOf(firstRemote.Reply).Pointer() != reflect.ValueOf(secondRemote.Reply).Pointer() {
		second.cancel()
		t.Fatal("closing a capability replaced the proxy while its parent was reserved")
	}
	second.activate()
	waitForClose(t, reply)
}

func TestValueCapabilityLimitRollsBackReservations(t *testing.T) {
	session := newSession("capability-limit", make(chan sessionUnhandledFrame), 0, make(chan struct{}), 0)
	t.Cleanup(func() { _ = session.Close() })
	reply := make(chan int)
	message := capabilityLimitPayload{}
	for index := range message.Replies {
		message.Replies[index] = reply
	}
	if _, err := prepareValidatedChannelValue(session, reflect.ValueOf(message)); !errors.Is(err, ErrPayloadLimit) {
		t.Fatalf("capability limit error = %v", err)
	}

	prepared, err := prepareValidatedChannelValue(session, reflect.ValueOf((chan<- int)(reply)))
	if err != nil {
		t.Fatalf("reservation after rollback: %v", err)
	}
	prepared.cancel()
}

func TestOversizedNativeSendTerminatesChannelExplicitly(t *testing.T) {
	client, server, _, _ := connectedChannels[string](t, 0, 0)
	oversized := strings.Repeat("x", maximumChannelPayloadSize)
	client.Send <- oversized
	if err := receiveWithTimeout(t, client.Errors); !errors.Is(err, ErrPayloadLimit) {
		t.Fatalf("oversized send error = %v", err)
	}

	waitForClose(t, client.Done)
	waitForClose(t, server.Receive)
	if err := client.Deliver("small"); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("Deliver after terminal encoding error = %v", err)
	}
}

func TestEmptyCapabilityIdentifierIsRejected(t *testing.T) {
	writer := wireWriter{}
	writer.byte(1)
	writer.string("", maximumWireIdentifierSize, "channel identifier")
	writer.byte(byte(channelSendOnly))
	writer.integer(0, "channel capacity")
	schema := schemaForType(reflect.TypeOf((chan<- int)(nil)))
	writer.bytes = append(writer.bytes, schema[:]...)
	payload, err := writer.result()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeChannelValue(nil, reflect.TypeOf((chan<- int)(nil)), payload); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("empty capability identifier error = %v", err)
	}
}

func TestSessionRejectsForeignSessionAndImpossibleAcknowledgement(t *testing.T) {
	tests := []struct {
		name  string
		frame networkFrame
	}{
		{name: "foreign session", frame: networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "foreign"}},
		{name: "impossible acknowledgement", frame: networkFrame{Version: protocolVersion, Kind: frameAcknowledgement, SessionID: "expected", Ack: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newSession("expected", make(chan sessionUnhandledFrame), 0, make(chan struct{}), 0)
			local, remote := net.Pipe()
			t.Cleanup(func() {
				_ = remote.Close()
				_ = session.Close()
			})
			if attached := session.attach(local); attached.err != nil {
				t.Fatal(attached.err)
			}
			if err := newFrameEncoder(remote).encode(test.frame); err != nil {
				t.Fatal(err)
			}
			if err := receiveWithTimeout(t, session.Errors()); !errors.Is(err, ErrMalformedFrame) {
				t.Fatalf("session protocol error = %v", err)
			}
			waitForClose(t, session.Done())
		})
	}
}

func TestRemoteChannelClosedPreservesErrorCategory(t *testing.T) {
	err := errorFromNetworkFrame(networkFrame{ErrorCode: protocolErrorChannelClosed, Error: ErrChannelClosed.Error()})
	if !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("remote channel close error = %v", err)
	}
}

func TestUnsupportedTypesAreRejected(t *testing.T) {
	if err := validateNetworkType(reflect.TypeOf((chan int)(nil))); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("bidirectional channel error = %v", err)
	}
	if err := validateNetworkType(reflect.TypeOf((chan<- int)(nil))); err != nil {
		t.Fatalf("send-only channel error = %v", err)
	}
	if err := validateNetworkType(reflect.TypeOf((<-chan int)(nil))); err != nil {
		t.Fatalf("receive-only channel error = %v", err)
	}
	if err := validateNetworkType(reflect.TypeOf((*any)(nil)).Elem()); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("interface error = %v", err)
	}
	if err := validateNetworkType(reflect.TypeOf(Channel[int]{})); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("nested Channel error = %v", err)
	}
	if err := validateNetworkType(reflect.TypeOf((*oversizedArrayType)(nil)).Elem()); !errors.Is(err, ErrPayloadLimit) {
		t.Fatalf("oversized array error = %v", err)
	}
}

func FuzzDecodeNetworkFrame(f *testing.F) {
	seed, err := encodeNetworkFrame(networkFrame{Version: protocolVersion, Kind: frameHeartbeat, SessionID: "seed"})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, payload []byte) { _, _ = decodeNetworkFrame(payload) })
}

////////////////////////////////////////////////////////////////////////////////
// END: Version 2 binary protocol tests
////////////////////////////////////////////////////////////////////////////////
