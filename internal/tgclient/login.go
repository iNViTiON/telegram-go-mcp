package tgclient

import (
	"context"

	"github.com/gotd/td/telegram/auth"
)

// SignIn runs the interactive authentication flow if the session is not yet
// authorized. Call it from inside Run.
//
// It only works on a client built with guard.PolicyLogin: PolicyRead denies
// every auth.* RPC, so calling this on the server's client fails with
// guard.ErrDenied. audit_test.go additionally bans the call in cmd/telegram-mcp.
func (c *Client) SignIn(ctx context.Context, ua auth.UserAuthenticator) error {
	return c.tg.Auth().IfNecessary(ctx, auth.NewFlow(ua, auth.SendCodeOptions{}))
}
