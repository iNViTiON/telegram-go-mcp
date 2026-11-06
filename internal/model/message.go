package model

import (
	"fmt"
	"strings"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

// Sender identifies who wrote a message.
type Sender struct {
	ID       string `json:"id"` // kind-prefixed, e.g. "user:1"
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
}

// Message is one message with everything the old server dropped: when it was
// written, by whom, in which topic, and what it answers.
type Message struct {
	ID       int     `json:"id"`
	Date     string  `json:"date"` // RFC3339, UTC
	EditDate string  `json:"edit_date,omitempty"`
	From     *Sender `json:"from,omitempty"`
	Out      bool    `json:"out,omitempty"`      // written by the signed-in user
	TopicID  int     `json:"topic_id,omitempty"` // forum topics only; 1 is General
	// ReplyToMsgID is set only for a real reply. A plain message in a topic is
	// not a reply to the topic's first message.
	ReplyToMsgID int    `json:"reply_to_msg_id,omitempty"`
	ForwardFrom  string `json:"forward_from,omitempty"`
	ViaBot       string `json:"via_bot,omitempty"`
	Text         string `json:"text,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	MediaName    string `json:"media_name,omitempty"`
	Service      string `json:"service,omitempty"` // join, pin, topic created, ...
	Replies      int    `json:"replies,omitempty"` // comments on a channel post, or replies in a thread
	Link         string `json:"link,omitempty"`    // opens the message in Telegram
}

// MessageContext is what a message needs from its chat and response.
type MessageContext struct {
	Forum     bool
	Ents      peer.Entities
	ChannelID int64  // set when the chat is a channel or supergroup
	Username  string // its public username, if any
}

// MessageOf converts one message. Empty (deleted) messages return false.
func MessageOf(m tg.MessageClass, c MessageContext) (Message, bool) {
	switch m := m.(type) {
	case *tg.Message:
		out := Message{
			ID:        m.ID,
			Date:      RFC3339(m.Date),
			Out:       m.Out,
			Text:      m.Message,
			MediaType: MediaType(m.Media),
			MediaName: mediaName(m.Media),
		}
		if e, ok := m.GetEditDate(); ok {
			out.EditDate = RFC3339(e)
		}
		out.From = c.sender(m)
		out.TopicID, out.ReplyToMsgID = topicAndReply(m, c.Forum)
		if f, ok := m.GetFwdFrom(); ok {
			out.ForwardFrom = c.forwardName(f)
		}
		if id, ok := m.GetViaBotID(); ok {
			if u, ok := c.Ents.User(id); ok && u.Username != "" {
				out.ViaBot = "@" + u.Username
			}
		}
		if r, ok := m.GetReplies(); ok {
			out.Replies = r.Replies
		}
		out.Link = c.link(m.PeerID, out.TopicID, m.ID)
		return out, true

	case *tg.MessageService:
		out := Message{ID: m.ID, Date: RFC3339(m.Date), Service: serviceKind(m.Action)}
		if a, ok := m.Action.(*tg.MessageActionTopicCreate); ok {
			out.Text = a.Title
		}
		out.TopicID, _ = serviceTopic(m, c.Forum)
		return out, true
	}
	return Message{}, false
}

// topicAndReply works out which topic a message belongs to and what, if
// anything, it replies to.
//
// In a forum, a message in a topic carries a reply header with forum_topic set.
// reply_to_top_id then names the topic and reply_to_msg_id the message being
// answered. When reply_to_top_id is absent the header is just the way a
// message addresses its topic: reply_to_msg_id is the topic id, and nothing is
// being replied to. A forum message with no header at all is in General (1).
func topicAndReply(m *tg.Message, forum bool) (topic, reply int) {
	if rt, ok := m.GetReplyTo(); ok {
		if h, ok := rt.(*tg.MessageReplyHeader); ok {
			if h.ReplyToPeerID == nil { // a reply into another chat is not a reply here
				reply = h.ReplyToMsgID
			}
			if forum && h.ForumTopic {
				topic = h.ReplyToTopID
				if topic == 0 {
					topic = h.ReplyToMsgID // no top id: reply_to_msg_id IS the topic
				}
				if reply == topic { // nothing is being replied to
					reply = 0
				}
			}
		}
	}
	if !forum {
		return 0, reply
	}
	if topic == 0 {
		topic = 1
	}
	return topic, reply
}

func serviceTopic(m *tg.MessageService, forum bool) (topic, reply int) {
	if !forum {
		return 0, 0
	}
	if rt, ok := m.GetReplyTo(); ok {
		if h, ok := rt.(*tg.MessageReplyHeader); ok && h.ForumTopic {
			if h.ReplyToTopID != 0 {
				return h.ReplyToTopID, 0
			}
			return h.ReplyToMsgID, 0
		}
	}
	return 1, 0
}

func serviceKind(a tg.MessageActionClass) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", a), "*tg.MessageAction")
}

func (c MessageContext) sender(m *tg.Message) *Sender {
	p, ok := m.GetFromID()
	if !ok {
		p = m.PeerID // a channel's own post
	}
	switch p := p.(type) {
	case *tg.PeerUser:
		s := &Sender{ID: PeerRef(p), Name: "(unknown)"}
		if u, ok := c.Ents.User(p.UserID); ok {
			s.Name, s.Username = userName(u), u.Username
		}
		return s
	case *tg.PeerChannel:
		s := &Sender{ID: PeerRef(p), Name: "(unknown)"}
		if ch, ok := c.Ents.Channel(p.ChannelID); ok {
			s.Name, s.Username = ch.Title, ch.Username
		}
		return s
	case *tg.PeerChat:
		s := &Sender{ID: PeerRef(p), Name: "(unknown)"}
		if g, ok := c.Ents.Chat(p.ChatID); ok {
			s.Name = g.Title
		}
		return s
	}
	return nil
}

func (c MessageContext) forwardName(f tg.MessageFwdHeader) string {
	if f.FromName != "" {
		return f.FromName
	}
	if id, ok := f.GetFromID(); ok {
		if s := (MessageContext{Ents: c.Ents}).senderOfPeer(id); s != nil {
			return s.Name
		}
	}
	return ""
}

func (c MessageContext) senderOfPeer(p tg.PeerClass) *Sender {
	return c.sender(&tg.Message{PeerID: p})
}

// link builds a t.me link when the message belongs to the channel being read.
// Comments on a channel post live in another chat and get no link.
func (c MessageContext) link(msgPeer tg.PeerClass, topic, id int) string {
	p, ok := msgPeer.(*tg.PeerChannel)
	if !ok || c.ChannelID == 0 || p.ChannelID != c.ChannelID {
		return ""
	}
	base := fmt.Sprintf("https://t.me/c/%d", c.ChannelID)
	if c.Username != "" {
		base = "https://t.me/" + c.Username
	}
	if topic > 1 {
		return fmt.Sprintf("%s/%d/%d", base, topic, id)
	}
	return fmt.Sprintf("%s/%d", base, id)
}

func mediaName(m tg.MessageMediaClass) string {
	d, ok := m.(*tg.MessageMediaDocument)
	if !ok {
		return ""
	}
	doc, ok := d.Document.(*tg.Document)
	if !ok {
		return ""
	}
	for _, a := range doc.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			return f.FileName
		}
	}
	return ""
}
