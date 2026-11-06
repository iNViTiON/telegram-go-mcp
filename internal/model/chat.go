package model

import (
	"strings"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

const snippetRunes = 200

// ChatFromDialog builds a Chat from a dialog plus the entities and top messages
// that came back in the same response. The last message is matched by id so a
// dialog never shows another chat's message.
func ChatFromDialog(d *tg.Dialog, ents peer.Entities, tops []tg.MessageClass) Chat {
	c := Chat{
		ID:             PeerRef(d.Peer),
		Pinned:         d.Pinned,
		Unread:         d.UnreadCount,
		UnreadMentions: d.UnreadMentionsCount,
	}
	describePeer(&c, d.Peer, ents)

	for _, m := range tops {
		if nm, ok := m.AsNotEmpty(); ok && nm.GetID() == d.TopMessage && PeerRef(nm.GetPeerID()) == c.ID {
			c.LastMessage = previewOf(m, c, ents)
			break
		}
	}
	return c
}

func describePeer(c *Chat, p tg.PeerClass, ents peer.Entities) {
	switch p := p.(type) {
	case *tg.PeerUser:
		c.Kind, c.Title = "user", "(unavailable)"
		if u, ok := ents.User(p.UserID); ok {
			c.Title, c.Username = userName(u), u.Username
			if u.Bot {
				c.Kind = "bot"
			}
		}
	case *tg.PeerChat:
		c.Kind, c.Title = "group", "(unavailable)"
		if g, ok := ents.Chat(p.ChatID); ok {
			c.Title = g.Title
		}
	case *tg.PeerChannel:
		c.Kind, c.Title = "channel", "(unavailable)"
		if ch, ok := ents.Channel(p.ChannelID); ok {
			c.Title, c.Username, c.Forum = ch.Title, ch.Username, ch.Forum
			if ch.Megagroup {
				c.Kind = "supergroup"
			}
		}
	}
}

func userName(u *tg.User) string {
	if u.Deleted {
		return "Deleted account"
	}
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

func previewOf(m tg.MessageClass, c Chat, ents peer.Entities) *MessagePreview {
	switch m := m.(type) {
	case *tg.Message:
		p := &MessagePreview{
			ID:        m.ID,
			Date:      RFC3339(m.Date),
			Out:       m.Out,
			Text:      Snippet(m.Message, snippetRunes),
			MediaType: MediaType(m.Media),
		}
		p.From = senderName(m, c, ents)
		return p
	case *tg.MessageService:
		return &MessagePreview{ID: m.ID, Date: RFC3339(m.Date), Service: true}
	}
	return nil
}

func senderName(m *tg.Message, c Chat, ents peer.Entities) string {
	if m.Out {
		return "me"
	}
	if from, ok := m.GetFromID(); ok {
		if u, ok := from.(*tg.PeerUser); ok {
			if user, ok := ents.User(u.UserID); ok {
				return userName(user)
			}
		}
	}
	if c.Kind == "user" || c.Kind == "bot" || c.Kind == "channel" {
		return c.Title // a DM's other side, or a channel's own post
	}
	return ""
}
