package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shortDir returns a directory with a short path: unix socket paths are limited
// to 108 bytes and t.TempDir() can exceed that. It removes exactly that dir.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "tgmcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type countOut struct {
	N int64 `json:"n"`
}

// backend is one MCP server with shared state: every session increments the
// same counter, which makes "one backend, many sessions" observable.
func backend() (*mcp.Server, *atomic.Int64) {
	var n atomic.Int64
	srv := mcp.NewServer(&mcp.Implementation{Name: "backend"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "count"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, countOut, error) {
		return nil, countOut{N: n.Add(1)}, nil
	})
	return srv, &n
}

func startOwner(t *testing.T) (path string, hub *Hub, srv *mcp.Server) {
	t.Helper()
	path = filepath.Join(shortDir(t), "sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ = backend()
	hub = NewHub(srv, ln)
	go hub.Serve(t.Context())
	t.Cleanup(func() { hub.Close(path) })
	return path, hub, srv
}

// shim runs Bridge between pipes and returns an MCP client session over them.
func shim(t *testing.T, path string) (*mcp.ClientSession, <-chan error) {
	t.Helper()
	conn, err := Dial(t.Context(), path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()   // client -> shim "stdin"
	outR, outW := io.Pipe() // shim "stdout" -> client
	done := make(chan error, 1)
	go func() {
		err := Bridge(t.Context(), conn, inR, outW)
		outW.Close()
		done <- err
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs, done
}

func callCount(t *testing.T, cs *mcp.ClientSession) int64 {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "count", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("count: %v %+v", err, res)
	}
	return int64(res.StructuredContent.(map[string]any)["n"].(float64))
}

func TestManyClientsShareOneBackend(t *testing.T) {
	path, hub, srv := startOwner(t)

	a, _ := shim(t, path)
	b, _ := shim(t, path)

	// The owner's own stdio session is just one more session on the same server.
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	own, err := mcp.NewClient(&mcp.Implementation{Name: "own"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int64]bool{}
	for _, cs := range []*mcp.ClientSession{a, b, own, a, b} {
		seen[callCount(t, cs)] = true
	}
	for i := int64(1); i <= 5; i++ {
		if !seen[i] {
			t.Errorf("counter value %d missing: sessions are not sharing one backend (%v)", i, seen)
		}
	}
	if hub.Active() != 2 {
		t.Errorf("active relayed sessions = %d, want 2", hub.Active())
	}
}

func TestCleanStdinEOFEndsSessionAndReturnsNil(t *testing.T) {
	path, hub, _ := startOwner(t)
	cs, done := shim(t, path)
	callCount(t, cs)

	cs.Close() // closes the client's side: the shim's stdin hits EOF
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean EOF must not be an error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bridge did not return after stdin closed (CloseWrite missing?)")
	}
	waitFor(t, func() bool { return hub.Active() == 0 }, "owner never saw the session end")
}

func TestOwnerGoneIsAnError(t *testing.T) {
	path := filepath.Join(shortDir(t), "sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { c, _ := ln.Accept(); c.Close() }() // owner "dies" at once

	conn, err := Dial(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe() // stdin stays open: the client has not finished
	defer inW.Close()

	errc := make(chan error, 1)
	go func() { errc <- Bridge(t.Context(), conn, inR, io.Discard) }()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrOwnerGone) {
			t.Fatalf("want ErrOwnerGone, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bridge hung although the owner closed")
	}
}

func TestListenReplacesStaleSocketAndIsPrivate(t *testing.T) {
	path := filepath.Join(shortDir(t), "sock")
	if err := os.WriteFile(path, []byte("left by a dead owner"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("stale file must be replaced: %v", err)
	}
	defer ln.Close()
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("socket is %#o, want 0600", st.Mode().Perm())
	}
}

func TestDialWaitsForALateOwner(t *testing.T) {
	path := filepath.Join(shortDir(t), "sock")
	go func() {
		time.Sleep(400 * time.Millisecond)
		if ln, err := net.Listen("unix", path); err == nil {
			t.Cleanup(func() { ln.Close() })
		}
	}()
	c, err := Dial(t.Context(), path, 3*time.Second)
	if err != nil {
		t.Fatalf("shim must retry until the owner's listener is up: %v", err)
	}
	c.Close()
}

func TestDialGivesUp(t *testing.T) {
	start := time.Now()
	if _, err := Dial(t.Context(), filepath.Join(shortDir(t), "nope"), 300*time.Millisecond); err == nil {
		t.Fatal("want an error when no owner exists")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("gave up too slowly")
	}
}

func TestWaitIdleHoldsWhileASessionIsOpen(t *testing.T) {
	path, hub, _ := startOwner(t)
	cs, _ := shim(t, path)
	callCount(t, cs)

	const linger = 300 * time.Millisecond
	returned := make(chan time.Time, 1)
	go func() { hub.WaitIdle(t.Context(), linger); returned <- time.Now() }()

	select {
	case <-returned:
		t.Fatal("WaitIdle returned while a session was open")
	case <-time.After(3 * linger):
	}

	closedAt := time.Now()
	cs.Close()
	select {
	case at := <-returned:
		if at.Sub(closedAt) < linger {
			t.Errorf("returned %v after the last session ended, want at least the linger %v", at.Sub(closedAt), linger)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle never returned after the last session ended")
	}
}

func TestCloseStopsAcceptingAndRemovesSocket(t *testing.T) {
	path, hub, _ := startOwner(t)
	hub.Close(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket file must be gone, stat err = %v", err)
	}
	if _, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		t.Error("must not accept connections after Close")
	}
}

func TestSameUserAcceptsOurOwnConnections(t *testing.T) {
	path, _, _ := startOwner(t)
	cs, _ := shim(t, path) // works only if the SO_PEERCRED check passes for our uid
	callCount(t, cs)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
