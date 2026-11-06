package tools

import (
	"context"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/model"
	"github.com/hisoft/telegram-go-mcp/internal/tgerrs"
)

// DraftEntry is one draft somewhere on the account.
type DraftEntry struct {
	ChatID  string      `json:"chat_id"`
	Chat    string      `json:"chat,omitempty"`
	TopicID int         `json:"topic_id,omitempty"` // 0 for a draft that belongs to the chat itself
	Draft   model.Draft `json:"draft"`
}

// ListDraftsOut is the list_drafts result.
type ListDraftsOut struct {
	Drafts []DraftEntry `json:"drafts"`
}

// listDrafts issues: messages.getAllDrafts.
//
// The reply fields are shown exactly as stored (reply_to_msg_id and
// reply_to_top_id), not tidied, so a draft saved here can be compared with one
// the official app wrote.
func (t *Tools) listDrafts(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListDraftsOut, error) {
	out := ListDraftsOut{Drafts: []DraftEntry{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}
	res, err := t.API.MessagesGetAllDrafts(ctx)
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}

	var updates []tg.UpdateClass
	switch r := res.(type) {
	case *tg.Updates:
		updates = r.Updates
		t.peers.Learn(peer.EntitiesFromResult(r))
	case *tg.UpdatesCombined:
		updates = r.Updates
		t.peers.Learn(peer.EntitiesFromResult(r))
	}

	for _, u := range updates {
		d, ok := u.(*tg.UpdateDraftMessage)
		if !ok {
			continue
		}
		draft := model.DraftOf(d.Draft)
		if draft == nil {
			continue
		}
		e := DraftEntry{ChatID: model.PeerRef(d.Peer), Draft: *draft}
		if top, ok := d.GetTopMsgID(); ok {
			e.TopicID = top
		}
		if c, ok := t.peers.Lookup(e.ChatID); ok {
			e.Chat = c.Title
		}
		out.Drafts = append(out.Drafts, e)
	}
	return nil, out, nil
}
