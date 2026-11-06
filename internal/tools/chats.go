package tools

import (
	"context"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iNViTiON/telegram-go-mcp/internal/model"
	"github.com/iNViTiON/telegram-go-mcp/internal/tgerrs"
)

const (
	defaultChatLimit = 30
	maxChatLimit     = 100 // large pages invite FLOOD_WAIT
)

// ListChatsIn are the list_chats arguments.
type ListChatsIn struct {
	Limit    int    `json:"limit,omitempty" jsonschema:"how many chats to return, 1-100, default 30"`
	Archived bool   `json:"archived,omitempty" jsonschema:"list the archive folder instead of the main list"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous call, to get the next page"`
}

// ListChatsOut is the list_chats result.
type ListChatsOut struct {
	Chats      []model.Chat `json:"chats"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// listChats issues: messages.getDialogs.
func (t *Tools) listChats(ctx context.Context, _ *mcp.CallToolRequest, in ListChatsIn) (*mcp.CallToolResult, ListChatsOut, error) {
	out := ListChatsOut{Chats: []model.Chat{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}

	limit := in.Limit
	if limit <= 0 {
		limit = defaultChatLimit
	}
	if limit > maxChatLimit {
		limit = maxChatLimit
	}

	req := &tg.MessagesGetDialogsRequest{Limit: limit, OffsetPeer: &tg.InputPeerEmpty{}}
	if in.Archived {
		req.SetFolderID(1)
	}
	if in.Cursor != "" {
		c, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, out, err
		}
		req.OffsetDate, req.OffsetID, req.OffsetPeer = c.Date, c.ID, c.inputPeer()
		req.ExcludePinned = true // otherwise Telegram repeats pinned chats on every page
	}

	res, err := t.API.MessagesGetDialogs(ctx, req)
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	var (
		dialogs tg.DialogClassArray
		tops    tg.MessageClassArray
		ents    peer.Entities
		slice   bool
	)
	switch r := res.(type) {
	case *tg.MessagesDialogs:
		dialogs, tops, ents = r.Dialogs, r.Messages, peer.EntitiesFromResult(r)
	case *tg.MessagesDialogsSlice:
		dialogs, tops, ents, slice = r.Dialogs, r.Messages, peer.EntitiesFromResult(r), true
	default: // dialogsNotModified: nothing to list
		return nil, out, nil
	}

	t.peers.Learn(ents)

	var last *tg.Dialog
	for _, d := range dialogs {
		if d, ok := d.(*tg.Dialog); ok { // skip archive-folder entries
			out.Chats = append(out.Chats, model.ChatFromDialog(d, ents, tops))
			last = d
		}
	}

	// Another page may exist only for a "slice" result that came back full.
	if slice && last != nil && len(out.Chats) >= limit {
		out.NextCursor = nextCursor(last, ents, tops)
	}
	return nil, out, nil
}

// nextCursor derives the next offsets the way gotd's own dialog iterator does:
// from the last dialog's top message and peer.
func nextCursor(last *tg.Dialog, ents peer.Entities, tops []tg.MessageClass) string {
	for _, m := range tops {
		nm, ok := m.AsNotEmpty()
		if !ok || nm.GetID() != last.TopMessage || model.PeerRef(nm.GetPeerID()) != model.PeerRef(last.Peer) {
			continue
		}
		ip, err := ents.ExtractPeer(last.Peer)
		if err != nil {
			return ""
		}
		if c, ok := cursorFor(nm.GetDate(), nm.GetID(), ip); ok {
			return encodeCursor(c)
		}
	}
	return ""
}
