// Package relay lets several telegram-mcp processes share one Telegram session.
//
// Claude Desktop starts more than one copy of an MCP server (chat, plus a
// shared one for Cowork and Code), and Telegram can invalidate a login key that
// two live connections use at once. So exactly one process, the owner, holds
// the instance lock and the Telegram connection. Every other copy is a shim: a
// byte bridge between its stdio and a unix socket served by the owner, where
// each connection is a full MCP session on the same server and the same guard.
//
// The socket lives in the 0700 state directory and is chmod 0600, and the owner
// also checks each peer's uid. Anything running as you could already read the
// session file, so this adds no new privilege boundary.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

// ErrOwnerGone means the owner closed the connection before our client did.
var ErrOwnerGone = errors.New("the telegram-mcp instance that owns the session went away")

// Listen binds the owner's socket. The caller must hold the instance lock, so
// any file already at path is a leftover from a dead owner.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Hub runs one MCP session per accepted connection, all on one server.
type Hub struct {
	srv    *mcp.Server
	ln     net.Listener
	active atomic.Int64
}

// NewHub serves srv on ln.
func NewHub(srv *mcp.Server, ln net.Listener) *Hub { return &Hub{srv: srv, ln: ln} }

// Active is the number of open relayed sessions.
func (h *Hub) Active() int { return int(h.active.Load()) }

// Serve accepts connections until the listener is closed.
func (h *Hub) Serve(ctx context.Context) {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		if err := sameUser(conn); err != nil {
			conn.Close()
			continue
		}
		ss, err := h.srv.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
		if err != nil {
			conn.Close()
			continue
		}
		h.active.Add(1)
		go func() {
			_ = ss.Wait()
			conn.Close()
			h.active.Add(-1)
		}()
	}
}

// WaitIdle blocks until no relayed session has been open for linger, or ctx ends.
func (h *Hub) WaitIdle(ctx context.Context, linger time.Duration) {
	poll := linger / 10
	if poll < 5*time.Millisecond {
		poll = 5 * time.Millisecond
	}
	if poll > 250*time.Millisecond {
		poll = 250 * time.Millisecond
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()

	var idleSince time.Time
	for {
		switch {
		case h.active.Load() != 0:
			idleSince = time.Time{}
		case idleSince.IsZero():
			idleSince = time.Now()
		case time.Since(idleSince) >= linger:
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Close stops accepting and removes the socket file. Do this BEFORE releasing
// the instance lock: afterwards a new owner may bind the same path, and a late
// unlink here would delete its socket.
func (h *Hub) Close(socketPath string) {
	_ = h.ln.Close()
	_ = os.Remove(socketPath)
}

// sameUser fails closed unless the peer runs as our uid.
func sameUser(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d is not %d", cred.Uid, os.Getuid())
	}
	return nil
}

// Dial connects to the owner, retrying for up to wait: a copy that lost the
// lock race may start before the owner's listener is up.
func Dial(ctx context.Context, path string, wait time.Duration) (*net.UnixConn, error) {
	deadline := time.Now().Add(wait)
	for {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			return c.(*net.UnixConn), nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Bridge copies in to the owner and the owner's replies to out. It returns nil
// when in reaches EOF and the owner has finished replying, and ErrOwnerGone if
// the owner closes while in is still open. The bytes are opaque: the shim knows
// nothing about MCP, so it cannot change what the guard allows.
func Bridge(ctx context.Context, conn *net.UnixConn, in io.Reader, out io.Writer) error {
	defer conn.Close()

	var stdinDone atomic.Bool
	toOwner := make(chan error, 1)
	fromOwner := make(chan error, 1)

	go func() {
		_, err := io.Copy(conn, in)
		stdinDone.Store(true)
		_ = conn.CloseWrite() // tells the owner this session is over
		toOwner <- err
	}()
	go func() {
		_, err := io.Copy(out, conn)
		fromOwner <- err
	}()

	select {
	case err := <-toOwner:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrOwnerGone, err)
		}
		// Clean EOF from our client: let the owner finish its last replies.
		select {
		case <-fromOwner:
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
		return nil
	case err := <-fromOwner:
		if stdinDone.Load() {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrOwnerGone, err)
		}
		return ErrOwnerGone
	case <-ctx.Done():
		return ctx.Err()
	}
}
