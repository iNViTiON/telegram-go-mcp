// Package tgclient holds the ONLY telegram.NewClient call in this program.
//
// Audit claim: every MTProto client here is built by New, and New always puts
// guard.Middleware first in the chain. internal/tgclient/audit_test.go proves
// by AST that no other constructor or sibling invoker (tg.NewClient, Pool, DC,
// QR) exists anywhere in the tree.
package tgclient

import (
	"context"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/hisoft/telegram-go-mcp/internal/guard"
)

// Version is reported to Telegram as the app version.
const Version = "0.1.0"

// Config is everything New needs. Secrets come from files, never argv or env.
type Config struct {
	AppID       int
	AppHash     string
	SessionPath string
	Policy      guard.Policy
	// Audit, if non-nil, sees every request that reaches the guard. It must
	// write to stderr only: stdout carries MCP framing.
	Audit func(method string, allowed bool)
}

// Client is a guarded Telegram client. It deliberately does not embed
// *telegram.Client, so Pool, DC, QR and friends are not reachable from it.
type Client struct {
	tg    *telegram.Client
	state *State
}

// New builds the guarded client. It does not connect; call Run.
func New(cfg Config) *Client {
	st := newState()

	// gotd's default logger is log.Nop, so nothing is written to stdout.
	opts := telegram.Options{
		Middlewares: []telegram.Middleware{
			guard.Middleware(cfg.Policy, cfg.Audit), // first: denials are never retried
		},
		NoUpdates:         true, // no updates manager: zero RPCs through the chain on connect
		SessionStorage:    &session.FileStorage{Path: cfg.SessionPath},
		OnConnectionState: st.onConnectionState,
		Device: telegram.DeviceConfig{
			DeviceModel: "telegram-go-mcp",
			AppVersion:  Version,
		},
	}

	return &Client{
		tg:    telegram.NewClient(cfg.AppID, cfg.AppHash, opts),
		state: st,
	}
}

// API returns the generated RPC client. Every call goes through the guard.
func (c *Client) API() *tg.Client { return c.tg.API() }

// Invoke sends a raw request through the guard.
func (c *Client) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return c.tg.Invoke(ctx, input, output)
}

// State exposes connection readiness.
func (c *Client) State() *State { return c.state }

// Run connects, calls f, and blocks until f returns or the connection dies
// permanently. When Run returns, the client is marked fatal.
func (c *Client) Run(ctx context.Context, f func(ctx context.Context) error) error {
	err := c.tg.Run(ctx, f)
	c.state.SetFatal(err)
	return err
}
