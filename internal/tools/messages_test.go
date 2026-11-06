package tools

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func chPeer() *tg.PeerChannel { return &tg.PeerChannel{ChannelID: 100} }

func forumMsg(id, date int, body string, from tg.PeerClass, hdr *tg.MessageReplyHeader) *tg.Message {
	m := &tg.Message{ID: id, Date: date, Message: body, PeerID: chPeer()}
	if from != nil {
		m.SetFromID(from)
	}
	if hdr != nil {
		m.SetReplyTo(hdr)
	}
	return m
}

// messagesFixture is a forum supergroup's history, newest first, covering every
// way a message can sit in a topic.
func messagesFixture() tg.MessagesMessagesClass {
	edited := forumMsg(29, 1700000290, "in topic 5", &tg.PeerUser{UserID: 1}, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5})
	edited.SetEditDate(1700000295)

	file := forumMsg(26, 1700000260, "", &tg.PeerUser{UserID: 2}, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5})
	file.Media = &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: "report.pdf"},
	}}}

	fwd := forumMsg(23, 1700000230, "passed on", &tg.PeerUser{UserID: 1}, nil)
	fwd.SetFwdFrom(tg.MessageFwdHeader{FromName: "Some Channel"})

	svc := &tg.MessageService{ID: 25, Date: 1700000250, PeerID: chPeer(), Action: &tg.MessageActionTopicCreate{Title: "Frontend"}}

	return &tg.MessagesMessages{
		Messages: []tg.MessageClass{
			forumMsg(30, 1700000300, "general hello", &tg.PeerUser{UserID: 2}, nil),
			edited,
			forumMsg(28, 1700000280, "answer", &tg.PeerUser{UserID: 2}, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 20, ReplyToTopID: 5}),
			file,
			svc,
			&tg.MessageEmpty{ID: 24},
			fwd,
		},
		Chats: []tg.ChatClass{&tg.Channel{ID: 100, AccessHash: 22, Title: "Dev Group", Megagroup: true, Forum: true}},
		Users: []tg.UserClass{
			&tg.User{ID: 1, AccessHash: 11, FirstName: "Alice", Username: "alice"},
			&tg.User{ID: 2, AccessHash: 12, FirstName: "Bob", LastName: "B"},
		},
	}
}

func withMessages(t *testing.T) (*harness, *stub) {
	t.Helper()
	h, s := learned(t)
	s.msgs = messagesFixture
	return h, s
}

func TestMessagesCarryDateSenderTopicAndReply(t *testing.T) {
	h, _ := withMessages(t)
	out := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100"}))

	if seq(h) != "messages.getHistory" {
		t.Errorf("a whole-chat read issued %s", seq(h))
	}
	if out.Chat != "Dev Group" || len(out.Messages) != 6 { // the deleted message is skipped
		t.Fatalf("chat=%q messages=%d", out.Chat, len(out.Messages))
	}
	byID := map[int]int{}
	for i, m := range out.Messages {
		byID[m.ID] = i
	}

	g := out.Messages[byID[30]]
	if g.Date != "2023-11-14T22:18:20Z" || g.From == nil || g.From.ID != "user:2" || g.From.Name != "Bob B" || g.TopicID != 1 || g.Text != "general hello" {
		t.Errorf("General message wrong: %+v / %+v", g, g.From)
	}
	if g.Link != "https://t.me/devgroup/30" { // the fixture chat has a public username
		t.Errorf("General link = %q", g.Link)
	}

	p := out.Messages[byID[29]]
	if p.TopicID != 5 || p.ReplyToMsgID != 0 || p.EditDate != "2023-11-14T22:18:15Z" || p.From.Username != "alice" {
		t.Errorf("plain topic message must not look like a reply: %+v", p)
	}
	if p.Link != "https://t.me/devgroup/5/29" {
		t.Errorf("topic link = %q", p.Link)
	}

	if r := out.Messages[byID[28]]; r.TopicID != 5 || r.ReplyToMsgID != 20 {
		t.Errorf("a reply inside topic 5: %+v", r)
	}
	if f := out.Messages[byID[26]]; f.MediaType != "document" || f.MediaName != "report.pdf" || f.Text != "" {
		t.Errorf("attachment: %+v", f)
	}
	if s := out.Messages[byID[25]]; s.Service != "TopicCreate" || s.Text != "Frontend" || s.From != nil {
		t.Errorf("service message: %+v", s)
	}
	if fw := out.Messages[byID[23]]; fw.ForwardFrom != "Some Channel" {
		t.Errorf("forwarded: %+v", fw)
	}
	for i := 1; i < len(out.Messages); i++ {
		if out.Messages[i-1].ID < out.Messages[i].ID {
			t.Errorf("messages must stay newest first")
		}
	}
}

func TestReadTopicUsesRepliesRequestWithPaging(t *testing.T) {
	h, s := withMessages(t)
	out := decode[MessagesOut](t, h.call(t, "read_messages",
		map[string]any{"chat_id": "channel:100", "topic_id": 5, "limit": 7, "before_id": 99}))

	if seq(h) != "messages.getReplies" {
		t.Errorf("a topic read issued %s", seq(h))
	}
	r := s.replies[0]
	if r.MsgID != 5 || r.OffsetID != 99 || r.Limit != 7 {
		t.Errorf("request = msg_id %d offset_id %d limit %d, want 5, 99, 7", r.MsgID, r.OffsetID, r.Limit)
	}
	if out.TopicID != 5 {
		t.Errorf("topic_id = %d", out.TopicID)
	}
	// Telegram returned 7 entries for limit 7: a full page, so an older page may
	// exist, and the cursor is the oldest NON-EMPTY id Telegram returned (23).
	if out.NextBeforeID != 23 {
		t.Errorf("next_before_id = %d, want 23", out.NextBeforeID)
	}
}

func TestReadGeneralFiltersTheOverallHistory(t *testing.T) {
	h, s := withMessages(t)
	out := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 1}))

	if seq(h) != "messages.getHistory" || len(s.replies) != 0 {
		t.Errorf("General is read from getHistory, issued %s", seq(h))
	}
	var ids []int
	for _, m := range out.Messages {
		if m.TopicID != 1 {
			t.Errorf("message %d belongs to topic %d, not General", m.ID, m.TopicID)
		}
		ids = append(ids, m.ID)
	}
	if len(ids) != 3 { // 30 (plain), 25 (service, no topic header), 23 (forwarded)
		t.Errorf("General messages = %v", ids)
	}
}

func TestPagingCursorFollowsTelegramNotTheFilter(t *testing.T) {
	h, _ := withMessages(t)
	// 7 entries come back. Even when filtering keeps only 3, the cursor must come
	// from everything Telegram returned, or paging would skip messages.
	out := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 7}))
	if len(out.Messages) != 3 || out.NextBeforeID != 23 {
		t.Errorf("kept %d messages, next_before_id=%d (want 3 and 23)", len(out.Messages), out.NextBeforeID)
	}
	// A page that is not full has no older neighbour.
	out = decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "limit": 50}))
	if out.NextBeforeID != 0 {
		t.Errorf("a short page must not offer a cursor, got %d", out.NextBeforeID)
	}
}

func TestMessageLimitsAreClamped(t *testing.T) {
	h, s := withMessages(t)
	decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "limit": 100000}))
	decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100"}))
	if s.history[0].Limit != maxMsgLimit || s.history[1].Limit != defaultMsgLimit {
		t.Errorf("limits = %d, %d", s.history[0].Limit, s.history[1].Limit)
	}
}

func TestReadThreadAndCommentsGetNoLinks(t *testing.T) {
	h, s := learned(t)
	s.msgs = func() tg.MessagesMessagesClass {
		// Comments live in the linked discussion group (channel 200), not in channel 100.
		c := &tg.Message{ID: 7, Date: 1700000070, Message: "nice post", PeerID: &tg.PeerChannel{ChannelID: 200}}
		c.SetFromID(&tg.PeerUser{UserID: 1})
		return &tg.MessagesMessages{
			Messages: []tg.MessageClass{c},
			Users:    []tg.UserClass{&tg.User{ID: 1, FirstName: "Alice"}},
		}
	}
	out := decode[MessagesOut](t, h.call(t, "read_thread", map[string]any{"chat_id": "channel:100", "message_id": 42}))

	if seq(h) != "messages.getReplies" || s.replies[0].MsgID != 42 {
		t.Errorf("read_thread issued %s (%+v)", seq(h), s.replies)
	}
	if len(out.Messages) != 1 || out.Messages[0].Link != "" || out.Messages[0].From.Name != "Alice" {
		t.Errorf("comment: %+v", out.Messages)
	}
}

func TestSearch(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantTop int
		general bool
	}{
		{"whole chat", map[string]any{"chat_id": "channel:100", "query": "hello"}, 0, false},
		{"one topic", map[string]any{"chat_id": "channel:100", "query": "hello", "topic_id": 5}, 5, false},
		{"General is filtered client-side", map[string]any{"chat_id": "channel:100", "query": "hello", "topic_id": 1}, 0, true},
	}
	for _, c := range cases {
		h, s := withMessages(t)
		out := decode[MessagesOut](t, h.call(t, "search_messages", c.args))
		if seq(h) != "messages.search" {
			t.Errorf("%s: issued %s", c.name, seq(h))
		}
		r := s.searches[0]
		top, _ := r.GetTopMsgID()
		if r.Q != "hello" || top != c.wantTop {
			t.Errorf("%s: q=%q top_msg_id=%d, want hello and %d", c.name, r.Q, top, c.wantTop)
		}
		if _, ok := r.Filter.(*tg.InputMessagesFilterEmpty); !ok {
			t.Errorf("%s: filter = %T", c.name, r.Filter)
		}
		if c.general {
			for _, m := range out.Messages {
				if m.TopicID != 1 {
					t.Errorf("%s: message %d is in topic %d", c.name, m.ID, m.TopicID)
				}
			}
		}
	}
}

// Everything refused must be refused before Telegram is contacted.
func TestMessageReadersRefuseBadInputWithoutAnyRequest(t *testing.T) {
	cases := []struct {
		name, tool string
		args       map[string]any
		want       string
	}{
		{"unknown chat", "read_messages", map[string]any{"chat_id": "channel:999"}, "list_chats"},
		{"malformed chat", "read_messages", map[string]any{"chat_id": "nope"}, "invalid chat_id"},
		{"topic in a chat without topics", "read_messages", map[string]any{"chat_id": "user:1", "topic_id": 2}, "has no topics"},
		{"negative topic", "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": -3}, "must be positive"},
		{"negative limit", "read_messages", map[string]any{"chat_id": "channel:100", "limit": -1}, "must not be negative"},
		{"thread without a message", "read_thread", map[string]any{"chat_id": "channel:100"}, "message_id"}, // the schema rejects it before the handler runs
		{"empty query", "search_messages", map[string]any{"chat_id": "channel:100", "query": "   "}, "query is empty"},
		{"huge query", "search_messages", map[string]any{"chat_id": "channel:100", "query": strings.Repeat("x", 300)}, "longer than"},
		{"search topic in a chat without topics", "search_messages", map[string]any{"chat_id": "user:1", "query": "x", "topic_id": 2}, "has no topics"},
	}
	for _, c := range cases {
		h, _ := withMessages(t)
		before := len(h.audit)
		res := h.call(t, c.tool, c.args)
		if !res.IsError || !strings.Contains(text(res), c.want) {
			t.Errorf("%s: want an error containing %q, got %q", c.name, c.want, text(res))
		}
		if len(h.audit) != before {
			t.Errorf("%s: a refused call reached Telegram: %v", c.name, h.audit[before:])
		}
	}
}

// The readers must never be able to write: only read requests may appear.
func TestMessageReadersIssueOnlyReads(t *testing.T) {
	h, s := withMessages(t)
	h.call(t, "read_messages", map[string]any{"chat_id": "channel:100"})
	h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 5})
	h.call(t, "read_thread", map[string]any{"chat_id": "channel:100", "message_id": 1})
	h.call(t, "search_messages", map[string]any{"chat_id": "channel:100", "query": "x"})

	allowed := map[string]bool{"messages.getHistory": true, "messages.getReplies": true, "messages.search": true}
	for _, a := range h.audit {
		if !allowed[a] {
			t.Errorf("a reader issued %s", a)
		}
	}
	if len(h.denied) != 0 || len(s.saves) != 0 {
		t.Errorf("denied=%v saves=%d", h.denied, len(s.saves))
	}
}
