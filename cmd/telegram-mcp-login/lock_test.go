package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iNViTiON/telegram-go-mcp/internal/instancelock"
)

// TestHelperProcess runs the real main() as a child with args from the env.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_TEST_CHILD") != "1" {
		t.Skip("helper process only")
	}
	os.Args = append([]string{"telegram-mcp-login"}, strings.Fields(os.Getenv("TELEGRAM_MCP_TEST_ARGS"))...)
	main()
	os.Exit(0)
}

// Signing in or probing while the server holds the session would open a second
// live connection on one auth key. Both must refuse, before any network or tty.
func TestLoginRefusesWhileServerHoldsTheSession(t *testing.T) {
	for _, args := range []string{"", "probe -for 1s"} {
		root := t.TempDir()
		cfgDir := filepath.Join(root, "cfg", "telegram-go-mcp")
		stateDir := filepath.Join(root, "state", "telegram-go-mcp")
		for _, d := range []string{cfgDir, stateDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "credentials"), []byte("TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		held, err := instancelock.Acquire(filepath.Join(stateDir, "lock")) // the running server
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(),
			"TELEGRAM_MCP_TEST_CHILD=1",
			"TELEGRAM_MCP_TEST_ARGS="+args,
			"XDG_CONFIG_HOME="+filepath.Join(root, "cfg"),
			"XDG_STATE_HOME="+filepath.Join(root, "state"),
		)
		done := make(chan struct{})
		var out []byte
		var runErr error
		go func() { out, runErr = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("args %q: did not refuse promptly (it likely tried to connect)", args)
		}
		held.Release()

		if runErr == nil {
			t.Errorf("args %q: must exit non-zero while the lock is held; output:\n%s", args, out)
		}
		if !strings.Contains(string(out), "already using this Telegram session") {
			t.Errorf("args %q: message should say who holds the session, got:\n%s", args, out)
		}
	}
}
