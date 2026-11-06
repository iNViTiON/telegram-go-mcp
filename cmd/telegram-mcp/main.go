// Command telegram-mcp is the MCP server: it reads the user's Telegram and can
// save drafts, over stdio. It cannot send, edit or delete anything: see
// internal/guard.
//
// Claude Desktop may start several copies. The first takes the instance lock
// and becomes the owner: it holds the only Telegram connection and also serves
// the others over a unix socket. Every later copy is a shim that relays its
// stdio to the owner. See internal/relay.
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
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/config"
	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/instancelock"
	"github.com/hisoft/telegram-go-mcp/internal/relay"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
	"github.com/hisoft/telegram-go-mcp/internal/tools"
)

const (
	readyTimeout    = 15 * time.Second
	shutdownTimeout = 5 * time.Second
	// The owner's own client may quit while other copies still use it.
	lingerAfterLast = 30 * time.Second
)

// dialWait is how long a shim waits for the owner's socket to appear. A var so
// tests can shorten it.
var dialWait = 5 * time.Second

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

	// Take the lock BEFORE building a Telegram client: a copy that loses must
	// never connect.
	lock, err := instancelock.Acquire(p.LockFile)
	switch {
	case err == nil:
		defer lock.Release() // runs after runOwner has closed the socket
		return runOwner(ctx, p, creds)
	case errors.Is(err, instancelock.ErrHeld):
		return runShim(ctx, p, err)
	default:
		return err
	}
}

func runOwner(ctx context.Context, p config.Paths, creds config.Credentials) error {
	c := tgclient.New(tgclient.Config{
		AppID:       creds.AppID,
		AppHash:     creds.AppHash,
		SessionPath: p.SessionFile,
		Policy:      guard.PolicyRead,
		Audit:       config.AuditFromEnv(os.Stderr),
	})

	tgCtx, cancelTG := context.WithCancel(ctx)
	defer cancelTG()
	tgDone := make(chan struct{})
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

	srv := tools.NewServer(tools.Deps{
		API:   c.API(),
		Ready: func(ctx context.Context) error { return c.State().WaitReady(ctx, readyTimeout) },
	}, tgclient.Version)

	// Serve other copies. Without the relay this process still works for its own client.
	var hub *relay.Hub
	if ln, err := relay.Listen(p.SocketFile); err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcp: relay disabled, other copies cannot connect:", err)
	} else {
		hub = relay.NewHub(srv, ln)
		go hub.Serve(ctx)
	}

	// Our own stdio is just one session among several.
	ss, err := srv.Connect(ctx, &mcp.StdioTransport{}, nil)
	if err == nil {
		waited := make(chan error, 1)
		go func() { waited <- ss.Wait() }()
		select {
		case err = <-waited:
		case <-ctx.Done():
			ss.Close()
		}
	}

	// Our client is gone. Keep serving relayed sessions until they all end, then
	// exit, so orphans cannot pile up across app restarts.
	if hub != nil && ctx.Err() == nil {
		hub.WaitIdle(ctx, lingerAfterLast)
	}
	if hub != nil {
		hub.Close(p.SocketFile) // before the lock is released, see Hub.Close
	}

	cancelTG()
	select {
	case <-tgDone:
	case <-time.After(shutdownTimeout):
		fmt.Fprintln(os.Stderr, "telegram-mcp: telegram client did not stop in time")
	}
	return clean(err)
}

// runShim relays stdio to the owner. If the owner cannot be reached it still
// speaks MCP, so its tools can explain why they are disabled.
func runShim(ctx context.Context, p config.Paths, lockErr error) error {
	conn, err := relay.Dial(ctx, p.SocketFile, dialWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcp: another instance owns the session but cannot be reached; running disabled:", err)
		srv := tools.NewServer(tools.Deps{
			Ready: func(context.Context) error { return lockErr },
		}, tgclient.Version)
		return clean(srv.Run(ctx, &mcp.StdioTransport{}))
	}

	fmt.Fprintln(os.Stderr, "telegram-mcp: another instance owns the session; relaying to it")
	err = relay.Bridge(ctx, conn, os.Stdin, os.Stdout)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// clean maps the normal ways a session ends to nil.
func clean(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
