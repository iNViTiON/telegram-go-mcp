package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/model"
	"github.com/hisoft/telegram-go-mcp/internal/tgerrs"
)

const (
	defaultMsgLimit = 30
	maxMsgLimit     = 100
	maxQueryRunes   = 256
)

// MessagesOut is the shared result of the message readers. Messages are newest
// first; pass NextBeforeID back as before_id for the next, older page.
type MessagesOut struct {
	Chat         string          `json:"chat"`
	TopicID      int             `json:"topic_id,omitempty"`
	Messages     []model.Message `json:"messages"`
	NextBeforeID int             `json:"next_before_id,omitempty"`
}

// ReadMessagesIn are the read_messages arguments.
type ReadMessagesIn struct {
	ChatID   string `json:"chat_id" jsonschema:"\"me\", \"@username\", or an id from list_chats such as user:1 or channel:123"`
	TopicID  int    `json:"topic_id,omitempty" jsonschema:"read only this forum topic (1 is General); omit to read the whole chat"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many messages, 1-100, default 30"`
	BeforeID int    `json:"before_id,omitempty" jsonschema:"only messages older than this message id; use next_before_id from the previous call"`
}

// readMessages issues: messages.getHistory for a whole chat or the General topic,
// messages.getReplies for another forum topic. Neither marks anything as read.
func (t *Tools) readMessages(ctx context.Context, _ *mcp.CallToolRequest, in ReadMessagesIn) (*mcp.CallToolResult, MessagesOut, error) {
	out := MessagesOut{Messages: []model.Message{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}
	limit, err := clampLimit(in.Limit, in.BeforeID)
	if err != nil {
		return nil, out, err
	}
	c, err := t.resolve(ctx, in.ChatID)
	if err != nil {
		return nil, out, err
	}
	out.Chat = c.Title
	if in.TopicID != 0 {
		if in.TopicID < 0 {
			return nil, out, errors.New("topic_id must be positive")
		}
		if !c.Forum {
			return nil, out, fmt.Errorf("%s has no topics, so topic_id does not apply", c.Title)
		}
		out.TopicID = in.TopicID
	}

	var res tg.MessagesMessagesClass
	if in.TopicID > 1 {
		// A topic is a message thread rooted at the topic's first message.
		res, err = t.API.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer: c.Input, MsgID: in.TopicID, OffsetID: in.BeforeID, Limit: limit,
		})
	} else {
		// A whole chat, or General: General is the chat's messages that belong to
		// no other topic, so it is read from the overall history and filtered.
		res, err = t.API.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: c.Input, OffsetID: in.BeforeID, Limit: limit,
		})
	}
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	return nil, t.convert(out, c, res, limit, in.TopicID == 1), nil
}

// ReadThreadIn are the read_thread arguments.
type ReadThreadIn struct {
	ChatID    string `json:"chat_id" jsonschema:"\"me\", \"@username\", or an id from list_chats"`
	MessageID int    `json:"message_id" jsonschema:"the message whose replies to read: a message in a group, or a channel post to read its comments"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many messages, 1-100, default 30"`
	BeforeID  int    `json:"before_id,omitempty" jsonschema:"only messages older than this message id; use next_before_id from the previous call"`
}

// readThread issues: messages.getReplies.
func (t *Tools) readThread(ctx context.Context, _ *mcp.CallToolRequest, in ReadThreadIn) (*mcp.CallToolResult, MessagesOut, error) {
	out := MessagesOut{Messages: []model.Message{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}
	if in.MessageID <= 0 {
		return nil, out, errors.New("message_id is required")
	}
	limit, err := clampLimit(in.Limit, in.BeforeID)
	if err != nil {
		return nil, out, err
	}
	c, err := t.resolve(ctx, in.ChatID)
	if err != nil {
		return nil, out, err
	}
	out.Chat = c.Title

	res, err := t.API.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
		Peer: c.Input, MsgID: in.MessageID, OffsetID: in.BeforeID, Limit: limit,
	})
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	return nil, t.convert(out, c, res, limit, false), nil
}

// SearchMessagesIn are the search_messages arguments.
type SearchMessagesIn struct {
	ChatID   string `json:"chat_id" jsonschema:"\"me\", \"@username\", or an id from list_chats"`
	Query    string `json:"query" jsonschema:"text to look for in this chat"`
	TopicID  int    `json:"topic_id,omitempty" jsonschema:"search only this forum topic"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many messages, 1-100, default 30"`
	BeforeID int    `json:"before_id,omitempty" jsonschema:"only messages older than this message id; use next_before_id from the previous call"`
}

// searchMessages issues: messages.search, in one chat.
func (t *Tools) searchMessages(ctx context.Context, _ *mcp.CallToolRequest, in SearchMessagesIn) (*mcp.CallToolResult, MessagesOut, error) {
	out := MessagesOut{Messages: []model.Message{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}
	q := strings.TrimSpace(in.Query)
	switch {
	case q == "":
		return nil, out, errors.New("query is empty")
	case len([]rune(q)) > maxQueryRunes:
		return nil, out, fmt.Errorf("query is longer than %d characters", maxQueryRunes)
	case in.TopicID < 0:
		return nil, out, errors.New("topic_id must be positive")
	}
	limit, err := clampLimit(in.Limit, in.BeforeID)
	if err != nil {
		return nil, out, err
	}
	c, err := t.resolve(ctx, in.ChatID)
	if err != nil {
		return nil, out, err
	}
	out.Chat = c.Title
	if in.TopicID != 0 {
		if !c.Forum {
			return nil, out, fmt.Errorf("%s has no topics, so topic_id does not apply", c.Title)
		}
		out.TopicID = in.TopicID
	}

	req := &tg.MessagesSearchRequest{
		Peer: c.Input, Q: q, Filter: &tg.InputMessagesFilterEmpty{}, OffsetID: in.BeforeID, Limit: limit,
	}
	if in.TopicID > 1 {
		req.SetTopMsgID(in.TopicID)
	}
	res, err := t.API.MessagesSearch(ctx, req)
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	return nil, t.convert(out, c, res, limit, in.TopicID == 1), nil
}

// clampLimit applies the default and the cap. Large pages invite FLOOD_WAIT.
func clampLimit(limit, beforeID int) (int, error) {
	if limit < 0 || beforeID < 0 {
		return 0, errors.New("limit and before_id must not be negative")
	}
	if limit == 0 {
		limit = defaultMsgLimit
	}
	if limit > maxMsgLimit {
		limit = maxMsgLimit
	}
	return limit, nil
}

// convert turns a Telegram result into the model-facing shape. onlyGeneral
// keeps just the messages that belong to General.
func (t *Tools) convert(out MessagesOut, c chat, res tg.MessagesMessagesClass, limit int, onlyGeneral bool) MessagesOut {
	mod, ok := res.AsModified()
	if !ok {
		return out
	}
	users, chats := tg.UserClassArray(mod.GetUsers()), tg.ChatClassArray(mod.GetChats())
	ents := peer.NewEntities(users.UserToMap(), chats.ChatToMap(), chats.ChannelToMap())
	t.peers.Learn(ents)

	mc := model.MessageContext{Forum: c.Forum, Ents: ents, Username: c.Username}
	if kind, id, ok := strings.Cut(c.Key, ":"); ok && kind == "channel" {
		mc.ChannelID, _ = strconv.ParseInt(id, 10, 64)
	}

	raw := mod.GetMessages()
	oldest := 0
	for _, m := range raw {
		if nm, ok := m.AsNotEmpty(); ok && (oldest == 0 || nm.GetID() < oldest) {
			oldest = nm.GetID()
		}
		msg, ok := model.MessageOf(m, mc)
		if !ok || (onlyGeneral && msg.TopicID != 1) {
			continue
		}
		out.Messages = append(out.Messages, msg)
	}
	// A full page may have an older neighbour. The cursor follows what Telegram
	// returned, not what was kept, so filtering cannot make paging skip messages.
	if len(raw) >= limit && oldest > 1 {
		out.NextBeforeID = oldest
	}
	return out
}
