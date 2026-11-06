package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile is subject to umask
		t.Fatal(err)
	}
}

func TestLoadCredentialsOK(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials")
	write(t, p, "# comment\nTELEGRAM_API_ID=12345\n\nTELEGRAM_API_HASH = abcdef0123\n", 0o600)

	c, err := LoadCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.AppID != 12345 || c.AppHash != "abcdef0123" {
		t.Fatalf("got %+v", c)
	}
}

func TestLoadCredentialsRefusesOpenPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o660, 0o666} {
		p := filepath.Join(t.TempDir(), "credentials")
		write(t, p, "TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=x\n", mode)
		if _, err := LoadCredentials(p); !errors.Is(err, ErrInsecurePermissions) {
			t.Errorf("mode %#o: want ErrInsecurePermissions, got %v", mode, err)
		}
	}
}

func TestLoadCredentialsMissingFileExplains(t *testing.T) {
	_, err := LoadCredentials(filepath.Join(t.TempDir(), "nope"))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_API_ID") {
		t.Fatalf("want an error that explains the file format, got %v", err)
	}
}

func TestLoadCredentialsRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"placeholder": "TELEGRAM_API_ID=<id>\nTELEGRAM_API_HASH=<hash>\n",
		"non-numeric": "TELEGRAM_API_ID=abc\nTELEGRAM_API_HASH=x\n",
		"zero id":     "TELEGRAM_API_ID=0\nTELEGRAM_API_HASH=x\n",
		"missing":     "TELEGRAM_API_ID=1\n",
		"unknown key": "TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=x\nTELEGRAM_API_HSAH=y\n",
		"no equals":   "TELEGRAM_API_ID\n",
		"empty hash":  "TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "credentials")
		write(t, p, body, 0o600)
		if _, err := LoadCredentials(p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "credentials")
	write(t, p, "TELEGRAM_API_ID=999\nTELEGRAM_API_HASH=supersecrethash\nBOGUS=1\n", 0o600)
	_, err := LoadCredentials(p)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "supersecrethash") {
		t.Fatalf("error leaks the hash: %v", err)
	}
}

func TestEnsureStateDir(t *testing.T) {
	base := t.TempDir()
	p := Paths{StateDir: filepath.Join(base, "state")}
	p.SessionFile = filepath.Join(p.StateDir, "session.json")

	if err := EnsureStateDir(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.StateDir)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("state dir is %#o, want 0700", st.Mode().Perm())
	}

	write(t, p.SessionFile, "{}", 0o644)
	if err := EnsureStateDir(p); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("loose session file: want ErrInsecurePermissions, got %v", err)
	}
	write(t, p.SessionFile, "{}", 0o600)
	if err := EnsureStateDir(p); err != nil {
		t.Fatalf("private session file: %v", err)
	}

	if err := os.Chmod(p.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStateDir(p); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("loose state dir: want ErrInsecurePermissions, got %v", err)
	}
}

func TestDefaultPathsHonourXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.CredentialsFile != "/x/cfg/telegram-go-mcp/credentials" || p.SessionFile != "/x/state/telegram-go-mcp/session.json" {
		t.Fatalf("got %+v", p)
	}
}

func TestAuditFromEnv(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("TELEGRAM_MCP_AUDIT", "")
	if AuditFromEnv(&buf) != nil {
		t.Fatal("audit must be off by default")
	}
	t.Setenv("TELEGRAM_MCP_AUDIT", "1")
	f := AuditFromEnv(&buf)
	f("messages.getDialogs#a0f4cb4f", true)
	f("messages.sendMessage#fef48f62", false)
	got := buf.String()
	if !strings.Contains(got, "allowed messages.getDialogs") || !strings.Contains(got, "DENIED  messages.sendMessage") {
		t.Fatalf("unexpected audit output: %q", got)
	}
}

func TestCheckPrivateRejectsFileOwnedBySomeoneElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivate(path); err != nil {
		t.Fatalf("own 0600 file must pass: %v", err)
	}
	real := getuid
	getuid = func() int { return real() + 1 }
	defer func() { getuid = real }()
	if err := CheckPrivate(path); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("a file owned by another uid must be refused, got %v", err)
	}
}
