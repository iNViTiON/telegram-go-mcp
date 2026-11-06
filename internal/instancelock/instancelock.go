// Package instancelock lets exactly one process hold the Telegram session.
//
// Telegram can invalidate an authorization key that is used by two live
// connections at once (AUTH_KEY_DUPLICATED), which forces a fresh login. The
// desktop app may start more than one copy of an MCP server, so the first
// process takes an exclusive advisory lock and the others must not connect.
package instancelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrHeld means another process already holds the lock.
var ErrHeld = errors.New("another telegram-mcp instance already holds the Telegram session")

// Lock is a held lock. The OS releases it if the process dies.
type Lock struct{ f *os.File }

// Acquire takes the lock at path without blocking. Mode 0600; the directory
// must already exist.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close() // closing the descriptor releases the flock
}
