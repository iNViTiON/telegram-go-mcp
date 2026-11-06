package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/model"
	"github.com/hisoft/telegram-go-mcp/internal/tgerrs"
)

const (
	defaultMsgLimit = 30
	maxMsgLimit     = 100
	maxQueryRunes   = 256

	// General has no thread of its own: its messages are the ones in no other
	// topic. They are found by scanning the chat's history, so the scan is
	// bounded: this many requests of maxMsgLimit messages per call.
	generalScanRequests = 5
)

// fetchPage asks Telegram for one page of messages older than offsetID.
type fetchPage func(offsetID, n int) (tg.MessagesMessagesClass, error)

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
			return nil, out, fmt.Errorf("%q has no topics, so topic_id does not apply", c.Title)
		}
		out.TopicID = in.TopicID
	}

	if in.TopicID == 1 {
		return t.readGeneral(out, c, in.BeforeID, limit, func(offset, n int) (tg.MessagesMessagesClass, error) {
			return t.API.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: c.Input, OffsetID: offset, Limit: n})
		})
	}

	var res tg.MessagesMessagesClass
	if in.TopicID > 1 {
		// A topic is a message thread rooted at the topic's first message.
		res, err = t.API.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer: c.Input, MsgID: in.TopicID, OffsetID: in.BeforeID, Limit: limit,
		})
	} else {
		// The whole chat, every topic together.
		res, err = t.API.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: c.Input, OffsetID: in.BeforeID, Limit: limit,
		})
	}
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	return nil, t.convert(out, c, res, limit, false), nil
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
		// Seen live: in a forum, only a topic's own id is a thread. An ordinary
		// message has no reply thread, and Telegram answers TOPIC_ID_INVALID.
		if c.Forum && tgerr.Is(err, "TOPIC_ID_INVALID") {
			return nil, out, fmt.Errorf("%s is a forum: its messages have no reply threads, only topics are threads. "+
				"Use read_messages with topic_id to read a topic, and a message's reply_to_msg_id to see what it answers", c.Title)
		}
		// Seen live: a one-to-one chat or a basic group answers PEER_ID_INVALID,
		// which here means "no reply threads", not "no access".
		if tgerr.Is(err, "PEER_ID_INVALID") && !strings.HasPrefix(c.Key, "channel:") {
			return nil, out, fmt.Errorf("%s has no reply threads: they exist only in supergroups and channels, not in one-to-one chats or basic groups. "+
				"Use read_messages, and a message's reply_to_msg_id to see what it answers", c.Title)
		}
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
			return nil, out, fmt.Errorf("%q has no topics, so topic_id does not apply", c.Title)
		}
		out.TopicID = in.TopicID
	}

	if in.TopicID == 1 {
		return t.readGeneral(out, c, in.BeforeID, limit, func(offset, n int) (tg.MessagesMessagesClass, error) {
			return t.API.MessagesSearch(ctx, &tg.MessagesSearchRequest{
				Peer: c.Input, Q: q, Filter: &tg.InputMessagesFilterEmpty{}, OffsetID: offset, Limit: n,
			})
		})
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
	return nil, t.convert(out, c, res, limit, false), nil
}

// readGeneral collects General's messages by scanning older and older pages of
// the chat until limit are found, the history ends, or the scan budget is used.
// One page rarely holds any: the newest 20 messages of a busy forum can all
// belong to other topics, and returning "0 messages" would mislead.
//
// The cursor always follows what Telegram returned, never what was kept, so the
// next call resumes exactly where this one stopped. If more than limit messages
// were found, the extras are dropped and the cursor is the oldest one kept.
func (t *Tools) readGeneral(out MessagesOut, c chat, before, limit int, fetch fetchPage) (*mcp.CallToolResult, MessagesOut, error) {
	offset := before
	for i := 0; i < generalScanRequests && len(out.Messages) < limit; i++ {
		res, err := fetch(offset, maxMsgLimit)
		if err != nil {
			return nil, out, tgerrs.Error(err)
		}
		page := t.convert(MessagesOut{Messages: []model.Message{}}, c, res, maxMsgLimit, true)
		out.Messages = append(out.Messages, page.Messages...)
		out.NextBeforeID = page.NextBeforeID
		if page.NextBeforeID == 0 { // a short page: the history has ended
			break
		}
		offset = page.NextBeforeID
	}
	if len(out.Messages) > limit {
		out.Messages = out.Messages[:limit]
		out.NextBeforeID = out.Messages[limit-1].ID
	}
	return nil, out, nil
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
