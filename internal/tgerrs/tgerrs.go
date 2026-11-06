// Package tgerrs turns Telegram and guard errors into text for the model.
//
// The text says what happened and what to do next. It never includes
// credentials, and it deliberately does not import telegram/auth: that keeps
// the "auth stays out of the server" review rule meaningful.
package tgerrs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tgerr"

	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/instancelock"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
)

// Message returns model-facing text for err.
func Message(err error) string {
	var denied *guard.DeniedError
	switch {
	case errors.As(err, &denied):
		return fmt.Sprintf("This server can only read Telegram and save drafts; %s is not permitted. This is a bug in the tool, not something to retry.", denied.Method)
	case errors.Is(err, instancelock.ErrHeld):
		return "Another telegram-mcp instance already holds the Telegram session, so this copy is disabled to avoid invalidating it. Use the other instance, or close it and restart this one."
	case errors.Is(err, tgclient.ErrNotReady), errors.Is(err, context.DeadlineExceeded):
		return "The Telegram connection is not ready yet. Try again in a few seconds."
	case errors.Is(err, context.Canceled):
		return "The request was cancelled."
	}

	if d, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Sprintf("Telegram rate limit. Retry in %d seconds.", int(d.Round(time.Second)/time.Second))
	}

	if rpc, ok := tgerr.As(err); ok {
		switch {
		case rpc.Code == 401, tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN", "AUTH_KEY_INVALID"):
			return "The Telegram session is not signed in or was revoked. The user must run telegram-mcp-login in a terminal."
		case tgerr.Is(err, "AUTH_KEY_DUPLICATED"):
			return "Telegram invalidated the session because it was used from two places at once. The user must run telegram-mcp-login again."
		case tgerr.Is(err, "CHANNEL_PRIVATE", "CHAT_FORBIDDEN", "CHAT_WRITE_FORBIDDEN", "PEER_ID_INVALID", "CHANNEL_INVALID", "USER_BANNED_IN_CHANNEL"):
			return fmt.Sprintf("No access to that chat (%s). Use list_chats to find a valid chat id.", rpc.Type)
		case tgerr.Is(err, "MSG_ID_INVALID", "TOPIC_ID_INVALID", "OFFSET_INVALID"):
			return fmt.Sprintf("Telegram rejected the id or offset (%s).", rpc.Type)
		}
		return fmt.Sprintf("Telegram error %s (code %d).", rpc.Type, rpc.Code)
	}

	return "Unexpected error: " + err.Error()
}

// Error wraps err as an error whose text is Message(err). Returning it from a
// tool handler makes the SDK report a tool error the model can read, rather
// than a protocol error.
func Error(err error) error { return errors.New(Message(err)) }
