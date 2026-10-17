//go:build !tinygo

package netchan

import (
	"bytes"
	"crypto/tls"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/matveynator/netchan/v2/internal/interop"
)

func TestTinyGoWireInteroperabilityThroughTLSOffload(t *testing.T) {
	binary := os.Getenv("NETCHAN_TINYGO_TEST_BINARY")
	if binary == "" {
		t.Skip("set NETCHAN_TINYGO_TEST_BINARY to the compiled interop fixture")
	}
	type task = interop.Task
	serverTLS, clientTLS := testTLSConfigurations(t)
	listener, err := Listen[task]("127.0.0.1:0", Config{TLS: serverTLS})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secured, err := tls.Dial("tcp", listener.Address, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer secured.Close()
	if state := secured.ConnectionState(); state.Version != tls.VersionTLS13 || len(state.VerifiedChains) == 0 {
		t.Fatal("broker did not authenticate TLS 1.3")
	}
	process := exec.Command(binary)
	var diagnostics bytes.Buffer
	process.Stderr = &diagnostics
	input, err := process.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	timeout := time.AfterFunc(20*time.Second, func() { _ = process.Process.Kill() })
	defer timeout.Stop()
	defer func() { _ = process.Process.Kill() }()
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(secured, output); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(input, secured); _ = input.Close(); copied <- struct{}{} }()
	stopped := make(chan struct{})
	go func() {
		channel, accepted := <-listener.Channels
		if !accepted {
			close(stopped)
			return
		}
		request, open := <-channel.Receive
		if open && request.Name == "tinygo" {
			request.Reply <- "Go TLS -> TinyGo"
			<-request.Stop
		}
		close(stopped)
	}()
	if err = process.Wait(); err != nil {
		t.Fatalf("TinyGo exchange: %v: %s", err, diagnostics.String())
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("TinyGo Stop capability did not close")
	}
	_ = secured.Close()
	_ = input.Close()
	_ = output.Close()
	<-copied
	<-copied
}
