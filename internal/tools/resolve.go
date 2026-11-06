package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/iNViTiON/telegram-go-mcp/internal/model"
	"github.com/iNViTiON/telegram-go-mcp/internal/peercache"
	"github.com/iNViTiON/telegram-go-mcp/internal/tgerrs"
)

// chat is a resolved chat_id: everything a handler needs to address it.
type chat struct {
	Input    tg.InputPeerClass
	Title    string
	Forum    bool   // supergroup with topics
	Key      string // kind-prefixed id; empty for Saved Messages
	Username string
}

// resolve turns a chat_id into a peer. A kind-prefixed id must already be known
// from list_chats, because Telegram needs the chat's access hash; "me" and
// "@username" need no prior knowledge.
//
// resolve issues: contacts.resolveUsername, only for an "@username".
func (t *Tools) resolve(ctx context.Context, raw string) (chat, error) {
	ref, err := peercache.ParseRef(raw)
	if err != nil {
		return chat{}, err
	}

	switch {
	case ref.Me:
		return chat{Input: &tg.InputPeerSelf{}, Title: "Saved Messages"}, nil

	case ref.Username != "":
		res, err := t.API.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: ref.Username})
		if err != nil {
			return chat{}, tgerrs.Error(err)
		}
		t.peers.Learn(peer.EntitiesFromResult(res))
		e, ok := t.peers.Lookup(model.PeerRef(res.Peer))
		if !ok {
			return chat{}, errors.New("Telegram resolved that username but returned no usable chat; try list_chats")
		}
		return chat{Input: e.InputPeer(), Title: e.Title, Forum: e.Forum, Key: e.Key, Username: e.Username}, nil
	}

	e, ok := t.peers.Lookup(ref.Key)
	if !ok {
		return chat{}, fmt.Errorf("unknown chat %q: call list_chats first so the server learns it, or use @username", ref.Key)
	}
	return chat{Input: e.InputPeer(), Title: e.Title, Forum: e.Forum, Key: e.Key, Username: e.Username}, nil
}
