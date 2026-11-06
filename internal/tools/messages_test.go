package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/hisoft/telegram-go-mcp/internal/model"
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

func TestShortPageMeansTheHistoryEnded(t *testing.T) {
	h, _ := withMessages(t)
	// General is scanned in pages of 100. Telegram returns 7 entries, a short page,
	// so there is nothing older: no cursor, and nothing was skipped.
	out := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 7}))
	if len(out.Messages) != 3 || out.NextBeforeID != 0 {
		t.Errorf("kept %d messages, next_before_id=%d (want 3 and 0)", len(out.Messages), out.NextBeforeID)
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

// longHistory is a forum of 'total' messages, newest first, whose General
// messages are exactly the ids in general; every other message is in topic 5.
// It honours offset_id and limit like Telegram does.
func longHistory(total int, general ...int) func(offsetID, n int) tg.MessagesMessagesClass {
	isGeneral := map[int]bool{}
	for _, id := range general {
		isGeneral[id] = true
	}
	return func(offsetID, n int) tg.MessagesMessagesClass {
		start := total
		if offsetID > 0 && offsetID-1 < start {
			start = offsetID - 1 // strictly older than offset_id
		}
		var msgs []tg.MessageClass
		for id := start; id >= 1 && len(msgs) < n; id-- {
			var hdr *tg.MessageReplyHeader
			if !isGeneral[id] {
				hdr = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 5}
			}
			msgs = append(msgs, forumMsg(id, 1700000000+id, "m", &tg.PeerUser{UserID: 1}, hdr))
		}
		return &tg.MessagesMessages{
			Messages: msgs,
			Chats:    []tg.ChatClass{&tg.Channel{ID: 100, AccessHash: 22, Title: "Dev Group", Megagroup: true, Forum: true}},
			Users:    []tg.UserClass{&tg.User{ID: 1, AccessHash: 11, FirstName: "Alice"}},
		}
	}
}

func ids(ms []model.Message) []int {
	var out []int
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// Seen live: the newest 20 messages of a busy forum all belong to other topics,
// so a single page of General was empty. The scan must keep going.
func TestGeneralKeepsScanningUntilItHasEnough(t *testing.T) {
	h, s := learned(t)
	s.msgsFor = longHistory(1000, 950, 850, 750, 20)

	out := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 3}))

	if got := ids(out.Messages); fmt.Sprint(got) != "[950 850 750]" {
		t.Errorf("General messages = %v, want [950 850 750]", got)
	}
	if len(s.history) != 3 {
		t.Fatalf("scanned with %d requests, want 3", len(s.history))
	}
	for i, wantOffset := range []int{0, 901, 801} {
		if s.history[i].OffsetID != wantOffset || s.history[i].Limit != maxMsgLimit {
			t.Errorf("request %d: offset_id=%d limit=%d, want offset_id=%d limit=%d", i, s.history[i].OffsetID, s.history[i].Limit, wantOffset, maxMsgLimit)
		}
	}
	if out.NextBeforeID != 701 { // the oldest message of the last page Telegram returned
		t.Errorf("next_before_id = %d, want 701", out.NextBeforeID)
	}
}

func TestGeneralScanIsBoundedAndResumable(t *testing.T) {
	h, s := learned(t)
	s.msgsFor = longHistory(1000, 950, 20)

	// 600 messages with nothing in General: the budget (5 requests) runs out.
	out := decode[MessagesOut](t, h.call(t, "read_messages",
		map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 3, "before_id": 700}))
	if len(out.Messages) != 0 || len(s.history) != generalScanRequests || out.NextBeforeID != 200 {
		t.Fatalf("messages=%d requests=%d next_before_id=%d, want 0, %d, 200", len(out.Messages), len(s.history), out.NextBeforeID, generalScanRequests)
	}

	// Resuming from that cursor reaches the end of the history and finds the last one.
	s.history = nil
	out = decode[MessagesOut](t, h.call(t, "read_messages",
		map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 3, "before_id": out.NextBeforeID}))
	if got := ids(out.Messages); fmt.Sprint(got) != "[20]" || out.NextBeforeID != 0 {
		t.Errorf("resumed: messages %v next_before_id=%d, want [20] and 0 (history ended)", got, out.NextBeforeID)
	}
}

// If one page holds more General messages than asked for, the extras are
// dropped and the cursor is the oldest one kept, so the next call neither
// skips nor repeats any.
func TestGeneralTruncationNeverSkipsOrRepeats(t *testing.T) {
	h, s := learned(t)
	s.msgsFor = longHistory(1000, 990, 985, 980, 975)

	first := decode[MessagesOut](t, h.call(t, "read_messages", map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 2}))
	if fmt.Sprint(ids(first.Messages)) != "[990 985]" || first.NextBeforeID != 985 {
		t.Fatalf("first page: %v next_before_id=%d, want [990 985] and 985", ids(first.Messages), first.NextBeforeID)
	}
	second := decode[MessagesOut](t, h.call(t, "read_messages",
		map[string]any{"chat_id": "channel:100", "topic_id": 1, "limit": 2, "before_id": first.NextBeforeID}))
	if fmt.Sprint(ids(second.Messages)) != "[980 975]" {
		t.Errorf("second page = %v, want [980 975]", ids(second.Messages))
	}
}

func TestSearchInGeneralScansToo(t *testing.T) {
	h, s := learned(t)
	s.msgsFor = longHistory(1000, 950, 850, 750)

	out := decode[MessagesOut](t, h.call(t, "search_messages",
		map[string]any{"chat_id": "channel:100", "query": "hello", "topic_id": 1, "limit": 3}))
	if fmt.Sprint(ids(out.Messages)) != "[950 850 750]" || len(s.searches) != 3 {
		t.Errorf("messages %v after %d search requests, want [950 850 750] after 3", ids(out.Messages), len(s.searches))
	}
	for i, r := range s.searches {
		if r.Q != "hello" {
			t.Errorf("search request %d lost the query: %q", i, r.Q)
		}
	}
	if len(s.history) != 0 {
		t.Errorf("a search must not read the history: %d getHistory requests", len(s.history))
	}
}

// Seen live: in a forum, getReplies on an ordinary message is TOPIC_ID_INVALID.
func TestReadThreadInAForumExplainsInsteadOfFailing(t *testing.T) {
	h, s := learned(t)
	s.repliesErr = tgerr.New(400, "TOPIC_ID_INVALID")

	res := h.call(t, "read_thread", map[string]any{"chat_id": "channel:100", "message_id": 8155})
	if !res.IsError {
		t.Fatal("want an error")
	}
	for _, want := range []string{"forum", "read_messages", "topic_id"} {
		if !strings.Contains(text(res), want) {
			t.Errorf("the explanation should mention %q: %q", want, text(res))
		}
	}
	if strings.Contains(text(res), "TOPIC_ID_INVALID") {
		t.Errorf("the raw Telegram code should not be all the model gets: %q", text(res))
	}

	// Outside a forum the same code keeps its generic message.
	h, s = learned(t)
	s.repliesErr = tgerr.New(400, "TOPIC_ID_INVALID")
	res = h.call(t, "read_thread", map[string]any{"chat_id": "user:1", "message_id": 5})
	if !res.IsError || !strings.Contains(text(res), "rejected") {
		t.Errorf("a non-forum chat should get the generic message, got %q", text(res))
	}
}

// Seen live: in a one-to-one chat or a basic group, getReplies answers
// PEER_ID_INVALID. That does not mean "no access"; those chats simply have no
// reply threads, which only supergroups and channels do. The generic message
// ("no access, use list_chats") sent the model on a pointless search.
func TestReadThreadExplainsChatsThatHaveNoThreads(t *testing.T) {
	for _, chatID := range []string{"user:1", "me"} {
		h, s := learned(t)
		s.repliesErr = tgerr.New(400, "PEER_ID_INVALID")

		res := h.call(t, "read_thread", map[string]any{"chat_id": chatID, "message_id": 5})
		if !res.IsError {
			t.Fatalf("%s: want an error", chatID)
		}
		for _, want := range []string{"no reply threads", "supergroups and channels", "read_messages"} {
			if !strings.Contains(text(res), want) {
				t.Errorf("%s: should mention %q: %q", chatID, want, text(res))
			}
		}
		if strings.Contains(text(res), "No access") || strings.Contains(text(res), "list_chats") {
			t.Errorf("%s: must not claim a missing access: %q", chatID, text(res))
		}
	}

	// A channel keeps the generic message: there it really can mean no access.
	h, s := learned(t)
	s.repliesErr = tgerr.New(400, "PEER_ID_INVALID")
	res := h.call(t, "read_thread", map[string]any{"chat_id": "channel:100", "message_id": 5})
	if !res.IsError || !strings.Contains(text(res), "No access") {
		t.Errorf("a channel should keep the generic message, got %q", text(res))
	}
}
