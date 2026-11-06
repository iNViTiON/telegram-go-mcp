package model

import (
	"testing"

	"github.com/gotd/td/tg"
)

func withReply(h *tg.MessageReplyHeader) *tg.Message {
	m := &tg.Message{ID: 1}
	if h != nil {
		m.SetReplyTo(h)
	}
	return m
}

// How a message's topic and reply are read from its header. Forum messages in a
// topic carry forum_topic; reply_to_top_id then names the topic. Without it,
// reply_to_msg_id IS the topic id and nothing is being replied to.
func TestTopicAndReply(t *testing.T) {
	cases := []struct {
		name         string
		forum        bool
		msg          *tg.Message
		topic, reply int
	}{
		{"forum, no header: General", true, withReply(nil), 1, 0},
		{"forum, plain message in a topic", true, withReply(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5}), 5, 0},
		{"forum, reply to a message inside a topic", true, withReply(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 20, ReplyToTopID: 5}), 5, 20},
		{"forum, reply to the topic root", true, withReply(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5, ReplyToTopID: 5}), 5, 0},
		{"forum, reply to a message in General", true, withReply(&tg.MessageReplyHeader{ReplyToMsgID: 9}), 1, 9},
		{"not a forum, a reply", false, withReply(&tg.MessageReplyHeader{ReplyToMsgID: 9}), 0, 9},
		{"not a forum, no header", false, withReply(nil), 0, 0},
		{"reply into another chat is not a reply here", false,
			withReply(&tg.MessageReplyHeader{ReplyToMsgID: 9, ReplyToPeerID: &tg.PeerUser{UserID: 3}}), 0, 0},
	}
	for _, c := range cases {
		topic, reply := topicAndReply(c.msg, c.forum)
		if topic != c.topic || reply != c.reply {
			t.Errorf("%s: topic=%d reply=%d, want topic=%d reply=%d", c.name, topic, reply, c.topic, c.reply)
		}
	}
}

func TestLinkOnlyForMessagesOfTheChannelBeingRead(t *testing.T) {
	c := MessageContext{ChannelID: 100}
	own := &tg.PeerChannel{ChannelID: 100}
	if got := c.link(own, 5, 29); got != "https://t.me/c/100/5/29" {
		t.Errorf("topic link = %q", got)
	}
	if got := c.link(own, 1, 30); got != "https://t.me/c/100/30" {
		t.Errorf("General link = %q", got)
	}
	if got := c.link(&tg.PeerChannel{ChannelID: 200}, 0, 1); got != "" {
		t.Errorf("comments live in another chat and must get no link, got %q", got)
	}
	if got := c.link(&tg.PeerUser{UserID: 1}, 0, 1); got != "" {
		t.Errorf("a DM has no link, got %q", got)
	}
	pub := MessageContext{ChannelID: 100, Username: "devgroup"}
	if got := pub.link(own, 5, 29); got != "https://t.me/devgroup/5/29" {
		t.Errorf("public link = %q", got)
	}
}
