package model

import "github.com/gotd/td/tg"

// Draft is the text waiting in a chat's or topic's compose box.
type Draft struct {
	Text string `json:"text"`
	// Date is when Telegram last stored the draft (RFC3339, UTC). Pass it back
	// as expected_draft_date to replace this exact draft and nothing newer.
	Date         string `json:"date"`
	ReplyToMsgID int    `json:"reply_to_msg_id,omitempty"`
}

// DraftOf returns nil when there is no draft with text in it.
func DraftOf(d tg.DraftMessageClass) *Draft {
	m, ok := d.(*tg.DraftMessage)
	if !ok || m.Message == "" {
		return nil
	}
	out := &Draft{Text: m.Message, Date: RFC3339(m.Date)}
	if rt, ok := m.GetReplyTo(); ok {
		if r, ok := rt.(*tg.InputReplyToMessage); ok {
			out.ReplyToMsgID = r.ReplyToMsgID
		}
	}
	return out
}

// Topic is one forum topic.
type Topic struct {
	ID             int    `json:"id"` // 1 is General
	Title          string `json:"title"`
	Closed         bool   `json:"closed,omitempty"`
	Pinned         bool   `json:"pinned,omitempty"`
	Hidden         bool   `json:"hidden,omitempty"`
	Unread         int    `json:"unread"`
	UnreadMentions int    `json:"unread_mentions,omitempty"`
	TopMessage     int    `json:"top_message,omitempty"`
	Draft          *Draft `json:"draft,omitempty"`
}

// TopicOf converts a forum topic.
func TopicOf(t *tg.ForumTopic) Topic {
	return Topic{
		ID:             t.ID,
		Title:          t.Title,
		Closed:         t.Closed,
		Pinned:         t.Pinned,
		Hidden:         t.Hidden,
		Unread:         t.UnreadCount,
		UnreadMentions: t.UnreadMentionsCount,
		TopMessage:     t.TopMessage,
		Draft:          DraftOf(t.Draft),
	}
}
