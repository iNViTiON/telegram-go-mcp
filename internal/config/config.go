// Package config resolves file locations and loads secrets.
//
// Secrets never come from argv or from claude_desktop_config.json (the desktop
// app rewrites that file). They live in a 0600 file, and this package refuses
// to read it, or to use the session file, if group or world can access it.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const appDir = "telegram-go-mcp"

// Paths are the resolved on-disk locations.
type Paths struct {
	ConfigDir       string // holds credentials
	StateDir        string // holds the session
	CredentialsFile string
	SessionFile     string
	LockFile        string // held by whichever process owns the session
	SocketFile      string // the owner serves other copies here
}

// DefaultPaths follows the XDG spec: $XDG_CONFIG_HOME (default ~/.config) and
// $XDG_STATE_HOME (default ~/.local/state).
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil && (os.Getenv("XDG_CONFIG_HOME") == "" || os.Getenv("XDG_STATE_HOME") == "") {
		return Paths{}, fmt.Errorf("cannot resolve home directory: %w", err)
	}
	cfgBase := envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	stateBase := envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))

	p := Paths{
		ConfigDir: filepath.Join(cfgBase, appDir),
		StateDir:  filepath.Join(stateBase, appDir),
	}
	p.CredentialsFile = filepath.Join(p.ConfigDir, "credentials")
	p.SessionFile = filepath.Join(p.StateDir, "session.json")
	p.LockFile = filepath.Join(p.StateDir, "lock")
	p.SocketFile = filepath.Join(p.StateDir, "sock")
	return p, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Credentials are the Telegram API id and hash from my.telegram.org.
type Credentials struct {
	AppID   int
	AppHash string
}

// ErrInsecurePermissions is returned when a secret file is group/world accessible.
var ErrInsecurePermissions = errors.New("file permissions too open")

// CheckPrivate fails unless path is owned by the current user and accessible
// by its owner only.
func CheckPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s is %#o, run: chmod 600 %s", ErrInsecurePermissions, path, perm, path)
	}
	// A private mode means nothing if someone else owns the file: they can
	// read it, or chmod it open later.
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d, not by you (uid %d)", ErrInsecurePermissions, path, sys.Uid, getuid())
	}
	return nil
}

// Setup resolves paths, loads credentials and prepares the state directory. It
// is the single startup path for both binaries.
func Setup() (Paths, Credentials, error) {
	p, err := DefaultPaths()
	if err != nil {
		return p, Credentials{}, err
	}
	creds, err := LoadCredentials(p.CredentialsFile)
	if err != nil {
		return p, Credentials{}, err
	}
	return p, creds, EnsureStateDir(p)
}

// LoadCredentials reads KEY=VALUE lines (# comments allowed). Error messages
// name keys and line numbers, never values.
func LoadCredentials(path string) (Credentials, error) {
	if err := CheckPrivate(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Credentials{}, fmt.Errorf("credentials file not found: %s\n"+
				"create it (chmod 600) with two lines:\n"+
				"  TELEGRAM_API_ID=<number from my.telegram.org>\n"+
				"  TELEGRAM_API_HASH=<hash from my.telegram.org>", path)
		}
		return Credentials{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Credentials{}, err
	}
	defer f.Close()
	return parseCredentials(f, path)
}

func parseCredentials(r io.Reader, name string) (Credentials, error) {
	var c Credentials
	var sawID, sawHash bool

	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return Credentials{}, fmt.Errorf("%s:%d: expected KEY=VALUE", name, n)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if strings.ContainsAny(val, "<>") {
			return Credentials{}, fmt.Errorf("%s:%d: %s still holds a placeholder", name, n, key)
		}
		switch key {
		case "TELEGRAM_API_ID":
			id, err := strconv.Atoi(val)
			if err != nil || id <= 0 {
				return Credentials{}, fmt.Errorf("%s:%d: TELEGRAM_API_ID must be a positive integer", name, n)
			}
			c.AppID, sawID = id, true
		case "TELEGRAM_API_HASH":
			if val == "" {
				return Credentials{}, fmt.Errorf("%s:%d: TELEGRAM_API_HASH is empty", name, n)
			}
			c.AppHash, sawHash = val, true
		default:
			return Credentials{}, fmt.Errorf("%s:%d: unknown key %q", name, n, key)
		}
	}
	if err := sc.Err(); err != nil {
		return Credentials{}, err
	}
	if !sawID || !sawHash {
		return Credentials{}, fmt.Errorf("%s: need both TELEGRAM_API_ID and TELEGRAM_API_HASH", name)
	}
	return c, nil
}

// EnsureStateDir creates the state directory 0700 and refuses a looser one. If
// a session file already exists it must be private too: gotd's FileStorage only
// applies 0600 when it creates the file, so it never tightens an existing one.
func EnsureStateDir(p Paths) error {
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		return err
	}
	if err := CheckPrivate(p.StateDir); err != nil {
		return err
	}
	if err := CheckPrivate(p.SessionFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// AuditFromEnv returns a stderr logger when TELEGRAM_MCP_AUDIT=1, else nil.
// Output goes to w (pass os.Stderr): stdout carries MCP framing.
func AuditFromEnv(w io.Writer) func(method string, allowed bool) {
	if os.Getenv("TELEGRAM_MCP_AUDIT") != "1" {
		return nil
	}
	return NewAuditLogger(w)
}

// NewAuditLogger writes one line per request: allowed or DENIED, then the method.
func NewAuditLogger(w io.Writer) func(method string, allowed bool) {
	return func(method string, allowed bool) {
		verdict := "allowed"
		if !allowed {
			verdict = "DENIED "
		}
		fmt.Fprintf(w, "audit: %s %s\n", verdict, method)
	}
}

// getuid is swapped in tests to stand for a different user.
var getuid = os.Getuid
