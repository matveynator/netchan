//go:build netchan_buildcheck

package main

import (
	"crypto/tls"
	"fmt"
	"github.com/matveynator/netchan/v2"
)

type task struct {
	Name  string
	Reply chan<- string
	Stop  <-chan struct{}
}

// This program exercises public entry points during compilation. Hardware and
// verified TLS engine initialization belong to the application on each board.
func main() {
	listener, err := netchan.Listen[task]("ble:///build-check", netchan.Config{
		TLS:          &tls.Config{MinVersion: tls.VersionTLS13},
		ChannelTypes: []netchan.ChannelType{netchan.ChannelElement[string](), netchan.ChannelElement[struct{}]()},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer listener.Close()
	for channel := range listener.Channels {
		for request := range channel.Receive {
			request.Reply <- request.Name
		}
	}
}
