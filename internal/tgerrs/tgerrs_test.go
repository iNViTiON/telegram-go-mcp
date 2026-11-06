package tgerrs

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tgerr"

	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/instancelock"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
)

func TestMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // substring the model must be given
	}{
		{"guard denial", &guard.DeniedError{Method: "messages.sendMessage"}, "messages.sendMessage is not permitted"},
		{"second instance", fmt.Errorf("start: %w", instancelock.ErrHeld), "Another telegram-mcp instance"},
		{"not ready", fmt.Errorf("wait: %w", tgclient.ErrNotReady), "not ready"},
		{"deadline", context.DeadlineExceeded, "not ready"},
		{"cancelled", context.Canceled, "cancelled"},
		{"flood wait", tgerr.New(420, "FLOOD_WAIT_30"), "Retry in 30 seconds"},
		{"unauthorized", tgerr.New(401, "AUTH_KEY_UNREGISTERED"), "telegram-mcp-login"},
		{"revoked", tgerr.New(401, "SESSION_REVOKED"), "telegram-mcp-login"},
		{"duplicated key", tgerr.New(406, "AUTH_KEY_DUPLICATED"), "two places at once"},
		{"private chat", tgerr.New(400, "CHANNEL_PRIVATE"), "list_chats"},
		{"bad id", tgerr.New(400, "MSG_ID_INVALID"), "MSG_ID_INVALID"},
		{"other rpc", tgerr.New(500, "INTERNAL"), "Telegram error INTERNAL (code 500)"},
		{"unknown", fmt.Errorf("boom"), "boom"},
	}
	for _, c := range cases {
		if got := Message(c.err); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not contain %q", c.name, got, c.want)
		}
	}
}

func TestErrorTextMatchesMessage(t *testing.T) {
	err := tgerr.New(401, "AUTH_KEY_UNREGISTERED")
	if Error(err).Error() != Message(err) {
		t.Fatal("Error must carry exactly Message(err), so the SDK shows the model the mapped text")
	}
}
