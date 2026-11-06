// Package guard is the single security boundary of this program.
//
// Every MTProto request issued by any client in this binary passes Allowed()
// before it can reach the network. The default is DENY. Widening what this
// program can do means adding one line to this file; there is no other way.
//
// Rules for editing this file:
//   - messages.saveDraft is the ONLY write, and it is text-only (draftIsTextOnly).
//   - Never allowlist a type whose name contains Invoke, InitConnection, Send,
//     Forward, Delete, Edit, Read, Set, Toggle, Create, Join, Leave, Import or
//     Export. TestNoWriteNames enforces this.
//   - gotd applies invokeWithLayer / invokeWithoutUpdates / initConnection BELOW
//     the middleware chain (telegram/internal/manager/conn.go), so requests
//     arrive here unwrapped. A wrapper type reaching this file means someone
//     hand-built one: it is denied like anything else.
package guard

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// Policy selects which set of RPCs a client may issue.
type Policy uint8

const (
	// PolicyRead is used by the long-running server: reads plus messages.saveDraft.
	PolicyRead Policy = iota
	// PolicyLogin is used by the one-shot sign-in binary: auth RPCs only.
	PolicyLogin
)

// ErrDenied is returned (wrapped in *DeniedError) for every blocked request.
var ErrDenied = errors.New("guard: RPC not permitted")

// DeniedError names the blocked method so tool results can say what was refused.
type DeniedError struct {
	Method string
	Policy Policy
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("guard: RPC not permitted: %s", e.Method)
}

func (e *DeniedError) Unwrap() error { return ErrDenied }

// Allowed is the whole policy. It is exported so tests can check every request
// type in the TL registry against it with no client and no network.
func Allowed(p Policy, input bin.Encoder) bool {
	switch p {
	case PolicyRead:
		return allowedRead(input)
	case PolicyLogin:
		return allowedLogin(input)
	}
	return false
}

// Middleware enforces p. audit, if non-nil, sees every request (allowed or not)
// and must only write to stderr.
func Middleware(p Policy, audit func(method string, allowed bool)) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			name := MethodName(input)
			ok := Allowed(p, input)
			if audit != nil {
				audit(name, ok)
			}
			if !ok {
				return &DeniedError{Method: name, Policy: p}
			}
			return next.Invoke(ctx, input, output)
		}
	})
}

// MethodName returns the TL method name, e.g. "messages.sendMessage#fef48f62".
func MethodName(input bin.Encoder) string {
	if n, ok := input.(interface{ TypeName() string }); ok {
		return n.TypeName()
	}
	return fmt.Sprintf("%T", input)
}

func allowedRead(input bin.Encoder) bool {
	switch r := input.(type) {
	// ---- reads ----
	case *tg.UsersGetUsersRequest: // whoami
		return true
	case *tg.MessagesGetDialogsRequest: // list_chats
		return true
	case *tg.MessagesGetPeerDialogsRequest: // current draft of one chat
		return true
	case *tg.MessagesGetHistoryRequest: // read_messages
		return true
	case *tg.MessagesGetRepliesRequest: // read_thread (forum topics + comments)
		return true
	case *tg.MessagesGetMessagesRequest: // fetch by id (basic groups, users)
		return true
	case *tg.ChannelsGetMessagesRequest: // fetch by id (channels)
		return true
	case *tg.MessagesSearchRequest: // search_messages
		return true
	case *tg.MessagesGetForumTopicsRequest: // list_topics
		return true
	case *tg.MessagesGetForumTopicsByIDRequest: // current draft of one topic
		return true
	case *tg.MessagesGetAllDraftsRequest: // list_drafts
		return true
	case *tg.MessagesGetDiscussionMessageRequest: // channel post -> comment thread
		return true
	case *tg.ContactsResolveUsernameRequest: // @name -> peer
		return true
	case *tg.ChannelsGetChannelsRequest: // refresh a stale channel access_hash
		return true

	// ---- the only write ----
	case *tg.MessagesSaveDraftRequest:
		return draftIsTextOnly(r)
	}
	return false
}

// draftIsTextOnly rejects draft fields that could attach media, a suggested
// post or an animation effect, and any reply target other than a plain message
// reply. The assistant may only stage text.
func draftIsTextOnly(r *tg.MessagesSaveDraftRequest) bool {
	if r.Media != nil || r.RichMessage != nil || r.Effect != 0 || !r.SuggestedPost.Zero() {
		return false
	}
	switch r.ReplyTo.(type) {
	case nil, *tg.InputReplyToMessage:
		return true
	}
	return false
}

func allowedLogin(input bin.Encoder) bool {
	switch input.(type) {
	case *tg.AuthSendCodeRequest,
		*tg.AuthResendCodeRequest,
		*tg.AuthSignInRequest,
		*tg.AuthCheckPasswordRequest,
		*tg.AccountGetPasswordRequest,
		*tg.AuthLogOutRequest,    // only reachable via an explicit logout subcommand
		*tg.UsersGetUsersRequest: // auth status check
		return true
	}
	return false
}
