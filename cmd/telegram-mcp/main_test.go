package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hisoft/telegram-go-mcp/internal/instancelock"
)

// TestHelperProcess is the real main(), run as a child by the tests below.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_TEST_CHILD") != "1" {
		t.Skip("helper process only")
	}
	main()
	os.Exit(0)
}

type rpcMsg struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// A copy that loses the instance lock must still speak clean MCP on stdout,
// explain itself in its tools, never dial Telegram, and exit when stdin closes.
func TestSecondInstanceIsDisabledButSpeaksCleanMCP(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "cfg", "telegram-go-mcp")
	stateDir := filepath.Join(root, "state", "telegram-go-mcp")
	for _, d := range []string{cfgDir, stateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	creds := filepath.Join(cfgDir, "credentials")
	if err := os.WriteFile(creds, []byte("TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	held, err := instancelock.Acquire(filepath.Join(stateDir, "lock")) // play the first instance
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(),
		"TELEGRAM_MCP_TEST_CHILD=1",
		"XDG_CONFIG_HOME="+filepath.Join(root, "cfg"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
	)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("server closed stdout early; stderr:\n%s", stderr.String())
			}
			return l
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for a reply; stderr:\n%s", stderr.String())
		}
		return ""
	}
	send := func(s string) { t.Helper(); _, _ = stdin.Write([]byte(s + "\n")) }

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`)

	var replies []string
	for i := 0; i < 3; i++ {
		l := next()
		var m rpcMsg
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdout line is not JSON (framing corrupted): %q", l)
		}
		replies = append(replies, l)
	}
	all := strings.Join(replies, "\n")
	for _, want := range []string{`"name":"telegram"`, `"whoami"`, `"list_chats"`, `"isError":true`, "Another telegram-mcp instance"} {
		if !strings.Contains(all, want) {
			t.Errorf("replies missing %s:\n%s", want, all)
		}
	}

	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after stdin close: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("server did not exit after stdin closed")
	}

	if !strings.Contains(stderr.String(), "credentials="+creds) {
		t.Errorf("startup line with resolved paths missing from stderr:\n%s", stderr.String())
	}
}
