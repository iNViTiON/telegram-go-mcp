// Command telegram-mcp is the MCP server: it reads the user's Telegram and can
// save drafts, over stdio. It cannot send, edit or delete anything: see
// internal/guard.
//
// stdout carries MCP framing and nothing else. Diagnostics go to stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/config"
	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/instancelock"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
	"github.com/hisoft/telegram-go-mcp/internal/tools"
)

const (
	readyTimeout    = 15 * time.Second
	shutdownTimeout = 5 * time.Second
)

func main() {
	log.SetOutput(os.Stderr) // a stray log line on stdout would corrupt MCP framing
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p, creds, err := config.Setup()
	if err != nil {
		return err
	}
	// The desktop app's environment may differ from your shell's, so say which
	// files this process resolved. Look for this line in the app's MCP log.
	fmt.Fprintf(os.Stderr, "telegram-mcp %s: credentials=%s session=%s\n", tgclient.Version, p.CredentialsFile, p.SessionFile)

	c := tgclient.New(tgclient.Config{
		AppID:       creds.AppID,
		AppHash:     creds.AppHash,
		SessionPath: p.SessionFile,
		Policy:      guard.PolicyRead,
		Audit:       config.AuditFromEnv(os.Stderr),
	})

	// One process per session: a second live connection can make Telegram
	// invalidate the auth key. A copy that loses the race still serves MCP, so
	// its tools can explain why they are disabled, but it never connects.
	lock, lockErr := instancelock.Acquire(filepath.Join(p.StateDir, "lock"))
	if lockErr != nil && !errors.Is(lockErr, instancelock.ErrHeld) {
		return lockErr
	}
	defer lock.Release()

	ready := func(ctx context.Context) error { return c.State().WaitReady(ctx, readyTimeout) }
	tgDone := make(chan struct{})
	tgCtx, cancelTG := context.WithCancel(ctx)
	defer cancelTG()

	if lockErr != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcp: another instance holds the session; running disabled")
		ready = func(context.Context) error { return lockErr }
		close(tgDone)
	} else {
		go func() {
			defer close(tgDone)
			err := c.Run(tgCtx, func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "telegram-mcp: telegram client stopped:", err)
			}
		}()
	}

	srv := tools.NewServer(tools.Deps{API: c.API(), Ready: ready}, tgclient.Version)
	err = srv.Run(ctx, &mcp.StdioTransport{})

	// stdin closed or a signal arrived: stop Telegram, but never hang on it, so
	// orphaned processes cannot pile up across app restarts.
	cancelTG()
	select {
	case <-tgDone:
	case <-time.After(shutdownTimeout):
		fmt.Fprintln(os.Stderr, "telegram-mcp: telegram client did not stop in time")
	}

	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
