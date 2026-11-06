// Command telegram-mcp-login signs in to Telegram once and checks the setup.
//
//	telegram-mcp-login               interactive sign-in (phone, code, 2FA)
//	telegram-mcp-login probe [-for D] connect with the server's read-only policy,
//	                                 print who you are, then log every RPC that
//	                                 passes the guard while idle
//
// Run it in a terminal yourself. Credentials are typed on /dev/tty and never
// touch argv, environment or this program's output.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"github.com/iNViTiON/telegram-go-mcp/internal/config"
	"github.com/iNViTiON/telegram-go-mcp/internal/guard"
	"github.com/iNViTiON/telegram-go-mcp/internal/instancelock"
	"github.com/iNViTiON/telegram-go-mcp/internal/tgclient"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if len(args) > 0 && args[0] == "probe" {
		fs := flag.NewFlagSet("probe", flag.ContinueOnError)
		idle := fs.Duration("for", 30*time.Second, "how long to stay connected and watch for RPCs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return probe(ctx, *idle)
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q (use no arguments to sign in, or: probe)", args[0])
	}
	return signIn(ctx)
}

func signIn(ctx context.Context) error {
	p, creds, err := config.Setup()
	if err != nil {
		return err
	}
	release, err := holdSession(p)
	if err != nil {
		return err
	}
	defer release()
	tty, err := newTTYAuth()
	if err != nil {
		return err
	}
	defer tty.Close()

	// Audit is always on here: sign-in is rare, and the log is how the login
	// allowlist was confirmed against what Telegram really needs.
	c := tgclient.New(tgclient.Config{
		AppID:       creds.AppID,
		AppHash:     creds.AppHash,
		SessionPath: p.SessionFile,
		Policy:      guard.PolicyLogin,
		Audit:       config.NewAuditLogger(os.Stderr),
	})

	return c.Run(ctx, func(ctx context.Context) error {
		if err := c.SignIn(ctx, tty); err != nil {
			return explain(err)
		}
		who, err := whoami(ctx, c)
		if err != nil {
			return explain(err)
		}
		fmt.Printf("signed in as %s\nsession saved to %s\n", who, p.SessionFile)
		return nil
	})
}

func probe(ctx context.Context, idle time.Duration) error {
	p, creds, err := config.Setup()
	if err != nil {
		return err
	}
	release, err := holdSession(p)
	if err != nil {
		return err
	}
	defer release()

	var seen, denied atomic.Int64
	logger := config.NewAuditLogger(os.Stderr)
	c := tgclient.New(tgclient.Config{
		AppID:       creds.AppID,
		AppHash:     creds.AppHash,
		SessionPath: p.SessionFile,
		Policy:      guard.PolicyRead, // the server's own policy
		Audit: func(method string, allowed bool) {
			seen.Add(1)
			if !allowed {
				denied.Add(1)
			}
			logger(method, allowed)
		},
	})

	return c.Run(ctx, func(ctx context.Context) error {
		if err := c.State().WaitReady(ctx, 30*time.Second); err != nil {
			return err
		}
		who, err := whoami(ctx, c)
		if err != nil {
			return explain(err)
		}
		fmt.Printf("connected as %s\n", who)

		before := seen.Load()
		fmt.Printf("idling %s; every RPC passing the guard is logged to stderr\n", idle)
		select {
		case <-time.After(idle):
		case <-ctx.Done():
		}
		fmt.Printf("RPCs through the guard while idle: %d (denied: %d)\n", seen.Load()-before, denied.Load())
		return nil
	})
}

// holdSession takes the same lock as the server. Signing in or probing while the
// server is connected would put two live connections on one auth key, which
// Telegram may answer by invalidating it.
func holdSession(p config.Paths) (release func(), err error) {
	l, err := instancelock.Acquire(p.LockFile)
	if errors.Is(err, instancelock.ErrHeld) {
		return nil, errors.New("telegram-mcp is already using this Telegram session (probably the desktop app).\n" +
			"Quit it fully from the tray, then run this again.")
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = l.Release() }, nil
}

func whoami(ctx context.Context, c *tgclient.Client) (string, error) {
	users, err := c.API().UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u, ok := u.(*tg.User); ok {
			name := strings.TrimSpace(u.FirstName + " " + u.LastName)
			if u.Username != "" {
				name += " (@" + u.Username + ")"
			}
			return fmt.Sprintf("%s [id %d]", name, u.ID), nil
		}
	}
	return "", errors.New("Telegram returned no user for self")
}

func explain(err error) error {
	if auth.IsUnauthorized(err) {
		return errors.New("not signed in: run telegram-mcp-login (no arguments) first")
	}
	if errors.Is(err, guard.ErrDenied) {
		return fmt.Errorf("%w\nthe guard blocked an RPC Telegram needs; this is the signal to review guard.go, not to disable the guard", err)
	}
	return err
}
