//go:build linux && !baremetal && !tinygo

package rfcomm

import (
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func querySDP(device, uuid string, done <-chan struct{}) (string, error) {
	address, err := net.ParseMAC(device)
	if err != nil || len(address) != 6 {
		return "", errors.New("netchan: invalid Bluetooth address")
	}
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, unix.BTPROTO_L2CAP)
	if err != nil {
		return "", err
	}
	if fd < 0 || fd > 0x7fffffff {
		_ = unix.Close(fd)
		return "", errors.New("netchan: invalid SDP descriptor")
	}
	file := os.NewFile(uintptr(fd), "netchan-sdp")
	if file == nil {
		_ = unix.Close(fd)
		return "", net.ErrClosed
	}
	defer file.Close()
	remote := &unix.SockaddrL2{PSM: 1}
	copy(remote.Addr[:], address)
	err = unix.Connect(fd, remote)
	deadline := time.Now().Add(10 * time.Second)
	for err == unix.EINPROGRESS || err == unix.EAGAIN || err == unix.EALREADY {
		select {
		case <-done:
			return "", net.ErrClosed
		default:
		}
		if !time.Now().Before(deadline) {
			return "", os.ErrDeadlineExceeded
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		ready, pollErr := unix.Poll(poll, 50)
		if pollErr == unix.EINTR {
			continue
		}
		if pollErr != nil {
			return "", pollErr
		}
		if ready == 0 {
			continue
		}
		result, socketErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if socketErr != nil {
			return "", socketErr
		}
		if result != 0 {
			return "", unix.Errno(result)
		}
		err = nil
	}
	if err != nil {
		return "", err
	}
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-done:
			_ = file.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watcherDone }()
	if err = file.SetDeadline(deadline); err != nil {
		return "", err
	}
	return readDescription(uuid, func(request []byte) ([]byte, error) {
		written, writeErr := file.Write(request)
		if writeErr != nil {
			return nil, writeErr
		}
		if written != len(request) {
			return nil, io.ErrShortWrite
		}
		response := make([]byte, 16384)
		count, readErr := file.Read(response)
		return response[:count], readErr
	})
}
