package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iNViTiON/telegram-go-mcp/internal/instancelock"
	"github.com/iNViTiON/telegram-go-mcp/internal/relay"
)

// TestHelperProcess is the real main(), run as a child by the tests below.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_TEST_CHILD") != "1" {
		t.Skip("helper process only")
	}
	dialWait = 300 * time.Millisecond
	main()
	os.Exit(0)
}

const (
	initMsg  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`
	readyMsg = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	listMsg  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	whoMsg   = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
)

// env is a throwaway XDG tree. Paths are short: unix sockets are limited to
// 108 bytes and t.TempDir() can exceed that.
type env struct {
	root, state, socket, lock, creds string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "tgmcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	e := env{root: root, state: filepath.Join(root, "state", "telegram-go-mcp")}
	cfg := filepath.Join(root, "cfg", "telegram-go-mcp")
	for _, d := range []string{cfg, e.state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.creds = filepath.Join(cfg, "credentials")
	if err := os.WriteFile(e.creds, []byte("TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.lock, e.socket = filepath.Join(e.state, "lock"), filepath.Join(e.state, "sock")
	return e
}

// child is the real main() with its stdio wired to the test.
type child struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin interface {
		Write([]byte) (int, error)
		Close() error
	}
	lines  chan string
	stderr *strings.Builder
	done   chan error
}

func (e env) start(t *testing.T) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(),
		"TELEGRAM_MCP_TEST_CHILD=1",
		"XDG_CONFIG_HOME="+filepath.Join(e.root, "cfg"),
		"XDG_STATE_HOME="+filepath.Join(e.root, "state"),
	)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	c := &child{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 32), stderr: &strings.Builder{}, done: make(chan error, 1)}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	go func() { c.done <- cmd.Wait() }()
	return c
}

func (c *child) send(s string) { c.t.Helper(); _, _ = c.stdin.Write([]byte(s + "\n")) }

// next returns the next stdout line, failing the test if it is not JSON: that
// is the framing check.
func (c *child) next() string {
	c.t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			c.t.Fatalf("stdout closed early; stderr:\n%s", c.stderr.String())
		}
		var v json.RawMessage
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			c.t.Fatalf("stdout line is not JSON (framing corrupted): %q", l)
		}
		return l
	case <-time.After(10 * time.Second):
		c.t.Fatalf("timed out waiting for a reply; stderr:\n%s", c.stderr.String())
	}
	return ""
}

func (c *child) handshake() (all string) {
	c.send(initMsg)
	c.send(readyMsg)
	c.send(listMsg)
	c.send(whoMsg)
	for i := 0; i < 3; i++ {
		all += c.next() + "\n"
	}
	return all
}

func (c *child) exit(within time.Duration) error {
	c.t.Helper()
	select {
	case err := <-c.done:
		return err
	case <-time.After(within):
		c.t.Fatalf("did not exit within %v; stderr:\n%s", within, c.stderr.String())
	}
	return nil
}

// fakeOwner is an in-process owner: it holds the lock, serves a tiny MCP
// server on the socket, and reports how many relayed sessions are open.
func fakeOwner(t *testing.T, e env) *relay.Hub {
	t.Helper()
	l, err := instancelock.Acquire(e.lock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Release() })

	srv := mcp.NewServer(&mcp.Implementation{Name: "telegram"}, nil)
	type out struct {
		ID string `json:"id"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, out, error) {
			return nil, out{ID: "user:7"}, nil
		})
	ln, err := relay.Listen(e.socket)
	if err != nil {
		t.Fatal(err)
	}
	hub := relay.NewHub(srv, ln)
	go hub.Serve(t.Context())
	t.Cleanup(func() { hub.Close(e.socket) })
	return hub
}

func TestShimRelaysToTheOwnerWithCleanFraming(t *testing.T) {
	e := newEnv(t)
	hub := fakeOwner(t, e)
	c := e.start(t)

	all := c.handshake()
	for _, want := range []string{`"name":"telegram"`, `"whoami"`, "user:7"} {
		if !strings.Contains(all, want) {
			t.Errorf("relayed replies missing %s:\n%s", want, all)
		}
	}
	if hub.Active() != 1 {
		t.Errorf("owner sees %d relayed sessions, want 1", hub.Active())
	}

	c.stdin.Close()
	if err := c.exit(10 * time.Second); err != nil {
		t.Errorf("shim must exit 0 when its client closes: %v\nstderr:\n%s", err, c.stderr.String())
	}
	if !strings.Contains(c.stderr.String(), "credentials="+e.creds) {
		t.Errorf("startup line with resolved paths missing:\n%s", c.stderr.String())
	}
}

func TestOwnerDyingMidSessionMakesTheShimFail(t *testing.T) {
	e := newEnv(t)
	l, err := instancelock.Acquire(e.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	ln, err := net.Listen("unix", e.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // an owner that crashes after reading the first request
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		bufio.NewReader(conn).ReadString('\n')
		conn.Close()
	}()

	c := e.start(t)
	c.send(initMsg)
	err = c.exit(10 * time.Second)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Fatalf("shim must exit non-zero when the owner dies, got %v", err)
	}
	if !strings.Contains(c.stderr.String(), "went away") {
		t.Errorf("stderr should say the owner went away:\n%s", c.stderr.String())
	}
}

// With a lock holder but no reachable socket, the copy still speaks clean MCP
// and its tools explain themselves, and it never dials Telegram.
func TestUnreachableOwnerDegradesWithAnExplanation(t *testing.T) {
	e := newEnv(t)
	l, err := instancelock.Acquire(e.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	c := e.start(t)
	all := c.handshake()
	for _, want := range []string{`"name":"telegram"`, `"whoami"`, `"list_chats"`, `"isError":true`, "could not reach it"} {
		if !strings.Contains(all, want) {
			t.Errorf("replies missing %s:\n%s", want, all)
		}
	}
	c.stdin.Close()
	if err := c.exit(10 * time.Second); err != nil {
		t.Errorf("exit after stdin close: %v\nstderr:\n%s", err, c.stderr.String())
	}
}
