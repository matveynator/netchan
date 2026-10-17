package transport

import "net"

// Lifetime transfers a successful dial from an attempt to its connection owner.
// Cancellation of losing attempts must never cancel the selected connection.
type Lifetime struct {
	Done     <-chan struct{}
	commands chan lifetimeCommand
}
type lifetimeCommand struct {
	promote bool
	reply   chan struct{}
}

func NewLifetime(options Options) *Lifetime {
	done := make(chan struct{})
	lifetime := &Lifetime{Done: done, commands: make(chan lifetimeCommand)}
	ownerDone := options.OwnerDone
	if ownerDone == nil {
		ownerDone = options.Done
	}
	go func() {
		defer close(done)
		cancel := options.Done
		for {
			select {
			case command := <-lifetime.commands:
				if !command.promote {
					close(command.reply)
					return
				}
				cancel = ownerDone
				close(command.reply)
			case <-cancel:
				return
			}
		}
	}()
	return lifetime
}

func (lifetime *Lifetime) Promote() error {
	reply := make(chan struct{})
	select {
	case lifetime.commands <- lifetimeCommand{promote: true, reply: reply}:
	case <-lifetime.Done:
		return net.ErrClosed
	}
	select {
	case <-reply:
		return nil
	case <-lifetime.Done:
		return net.ErrClosed
	}
}
func (lifetime *Lifetime) Close() {
	reply := make(chan struct{})
	select {
	case lifetime.commands <- lifetimeCommand{reply: reply}:
	case <-lifetime.Done:
		return
	}
	<-lifetime.Done
}
