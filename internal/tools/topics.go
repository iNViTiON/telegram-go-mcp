package tools

import (
	"context"
	"errors"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/model"
	"github.com/hisoft/telegram-go-mcp/internal/tgerrs"
)

const (
	defaultTopicLimit = 50
	maxTopicLimit     = 100
)

// ListTopicsIn are the list_topics arguments.
type ListTopicsIn struct {
	ChatID string `json:"chat_id" jsonschema:"a forum supergroup: an id from list_chats such as channel:123, or @username"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many topics to return, 1-100, default 50"`
}

// ListTopicsOut is the list_topics result.
type ListTopicsOut struct {
	Chat   string        `json:"chat"`
	Count  int           `json:"count"` // Telegram's reported total; it does not always equal len(topics)
	Topics []model.Topic `json:"topics"`
}

// listTopics issues: messages.getForumTopics (plus contacts.resolveUsername for an @username).
func (t *Tools) listTopics(ctx context.Context, _ *mcp.CallToolRequest, in ListTopicsIn) (*mcp.CallToolResult, ListTopicsOut, error) {
	out := ListTopicsOut{Topics: []model.Topic{}}
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}
	c, err := t.resolve(ctx, in.ChatID)
	if err != nil {
		return nil, out, err
	}
	out.Chat = c.Title
	if !c.Forum {
		return nil, out, errors.New(c.Title + " has no topics (it is not a forum supergroup)")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = defaultTopicLimit
	}
	if limit > maxTopicLimit {
		limit = maxTopicLimit
	}

	res, err := t.API.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: c.Input, Limit: limit})
	if err != nil {
		return nil, out, tgerrs.Error(err)
	}
	t.peers.Learn(peer.EntitiesFromResult(res))

	out.Count = res.Count
	for _, tp := range res.Topics {
		if tp, ok := tp.(*tg.ForumTopic); ok { // skip deleted topics
			out.Topics = append(out.Topics, model.TopicOf(tp))
		}
	}
	return nil, out, nil
}
