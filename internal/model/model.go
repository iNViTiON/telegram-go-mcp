// Package model holds the JSON shapes returned to the model. Every message
// carries its id, date, sender and topic: the old server returned bare text.
package model

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

// Chat is one entry of list_chats.
type Chat struct {
	// ID is kind-prefixed ("user:1", "chat:2", "channel:3") because raw ids
	// collide across kinds.
	ID             string          `json:"id"`
	Kind           string          `json:"kind"` // user | bot | group | supergroup | channel
	Title          string          `json:"title"`
	Username       string          `json:"username,omitempty"`
	Forum          bool            `json:"forum,omitempty"` // has topics; use list_topics
	Pinned         bool            `json:"pinned,omitempty"`
	Unread         int             `json:"unread"`
	UnreadMentions int             `json:"unread_mentions,omitempty"`
	LastMessage    *MessagePreview `json:"last_message,omitempty"`
}

// MessagePreview is a short view of a chat's newest message.
type MessagePreview struct {
	ID        int    `json:"id"`
	Date      string `json:"date"` // RFC3339, UTC
	From      string `json:"from,omitempty"`
	Out       bool   `json:"out,omitempty"` // sent by the signed-in user
	Text      string `json:"text,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Service   bool   `json:"service,omitempty"` // join, pin, topic created, ...
}

// RFC3339 formats a Telegram unix timestamp. Zero yields "".
func RFC3339(unix int) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(int64(unix), 0).UTC().Format(time.RFC3339)
}

// PeerRef renders a peer as a kind-prefixed id.
func PeerRef(p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("user:%d", p.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d", p.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d", p.ChannelID)
	}
	return ""
}

// Snippet collapses whitespace and cuts s to at most max runes.
func Snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// MediaType names a message's attachment kind, or "" for none.
func MediaType(m tg.MessageMediaClass) string {
	switch m := m.(type) {
	case nil, *tg.MessageMediaEmpty:
		return ""
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return documentKind(m)
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location"
	case *tg.MessageMediaContact:
		return "contact"
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaWebPage:
		return "webpage"
	case *tg.MessageMediaDice:
		return "dice"
	}
	return "media"
}

func documentKind(m *tg.MessageMediaDocument) string {
	doc, ok := m.Document.(*tg.Document)
	if !ok {
		return "document"
	}
	for _, a := range doc.Attributes {
		switch a := a.(type) {
		case *tg.DocumentAttributeSticker:
			return "sticker"
		case *tg.DocumentAttributeVideo:
			if a.RoundMessage {
				return "video_note"
			}
			return "video"
		case *tg.DocumentAttributeAudio:
			if a.Voice {
				return "voice"
			}
			return "audio"
		case *tg.DocumentAttributeAnimated:
			return "gif"
		}
	}
	return "document"
}
