package tgclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
)

// ErrNotReady is returned by WaitReady when the connection did not become
// ready in time. Tool handlers map it to an actionable message.
var ErrNotReady = errors.New("telegram connection not ready")

// State tracks the primary connection so tool handlers can wait for readiness
// instead of parking inside the client until a context expires.
type State struct {
	mu       sync.Mutex
	ready    chan struct{} // closed while the connection is ready
	fatal    error
	fatalCh  chan struct{} // closed once Run has returned
	fatalSet bool
}

func newState() *State {
	return &State{
		ready:   make(chan struct{}),
		fatalCh: make(chan struct{}),
	}
}

// onConnectionState is wired into telegram.Options.OnConnectionState.
func (s *State) onConnectionState(cs telegram.ConnectionState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	isClosed := false
	select {
	case <-s.ready:
		isClosed = true
	default:
	}

	switch cs {
	case telegram.ConnectionStateReady:
		if !isClosed {
			close(s.ready)
		}
	default: // connecting, disconnected
		if isClosed {
			s.ready = make(chan struct{})
		}
	}
}

// SetFatal records that the client stopped for good (e.g. AUTH_KEY_UNREGISTERED).
func (s *State) SetFatal(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatalSet {
		return
	}
	s.fatalSet = true
	s.fatal = err
	close(s.fatalCh)
}

// Fatal returns the error the client stopped with, or nil while it is running.
func (s *State) Fatal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fatal
}

// WaitReady blocks until the connection is ready, d elapses, ctx is done, or
// the client has stopped.
func (s *State) WaitReady(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	ready, fatalCh := s.ready, s.fatalCh
	s.mu.Unlock()

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ready:
		return nil
	case <-fatalCh:
		if err := s.Fatal(); err != nil {
			return fmt.Errorf("telegram client stopped: %w", err)
		}
		return errors.New("telegram client stopped")
	case <-t.C:
		return ErrNotReady
	case <-ctx.Done():
		return ctx.Err()
	}
}
