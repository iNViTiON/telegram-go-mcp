package tools

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/iNViTiON/telegram-go-mcp/internal/model"
)

const (
	rpcPeerDialogs = "messages.getPeerDialogs"
	rpcTopicByID   = "messages.getForumTopicsByID"
	rpcSaveDraft   = "messages.saveDraft"
)

func seq(h *harness) string { return strings.Join(h.audit, ",") }

func countRPC(h *harness, name string) int {
	n := 0
	for _, a := range h.audit {
		if a == name {
			n++
		}
	}
	return n
}

// learned returns a harness whose server has already seen the fixture chats
// (user:1 and the forum supergroup channel:100), as after a list_chats call.
func learned(t *testing.T) (*harness, *stub) {
	t.Helper()
	s := &stub{
		dialogs: func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass { return dialogsFixture() },
	}
	h := newHarness(t, s, nil)
	decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{}))
	h.audit = nil
	return h, s
}

func saveArgs(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestSaveDraftToSavedMessages(t *testing.T) {
	s := &stub{}
	h := newHarness(t, s, nil)

	out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "text", "buy milk")))

	if got, want := seq(h), "messages.getPeerDialogs,messages.saveDraft,messages.getPeerDialogs"; got != want {
		t.Errorf("request sequence = %s, want read, save, read-back: %s", got, want)
	}
	if !out.Saved || out.Where != "Saved Messages" || out.Draft == nil || out.Draft.Text != "buy milk" || out.Draft.Date == "" {
		t.Errorf("result wrong: %+v", out)
	}
	if !strings.Contains(out.Note, "Nothing was sent") {
		t.Errorf("the note must say nothing was sent: %q", out.Note)
	}
	if len(s.saves) != 1 || s.saves[0].Message != "buy milk" {
		t.Errorf("saveDraft requests = %+v", s.saves)
	}
	if _, has := s.saves[0].GetReplyTo(); has {
		t.Error("a plain draft must have no reply target")
	}
}

func TestSaveDraftIntoAForumTopic(t *testing.T) {
	h, s := learned(t)
	s.topicTitles = map[int]string{5: "Frontend"}

	out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "channel:100", "topic_id", 5, "text", "ship it")))

	if got, want := seq(h), "messages.getForumTopicsByID,messages.saveDraft,messages.getForumTopicsByID"; got != want {
		t.Errorf("request sequence = %s, want %s", got, want)
	}
	if out.Where != "Dev Group › Frontend" {
		t.Errorf("where = %q, want the chat and topic titles so the user knows where to look", out.Where)
	}
	// The shape the official app writes for a plain draft in a topic, read back
	// from drafts typed by hand: reply_to_msg_id 0 and top_msg_id = the topic.
	// Putting the topic id in reply_to_msg_id too displays as a reply to the
	// topic's first message; dropping top_msg_id saved without error and then
	// appeared in no topic (both seen live).
	rt, ok := s.saves[0].ReplyTo.(*tg.InputReplyToMessage)
	if !ok {
		t.Fatalf("topic draft needs a reply target, got %T", s.saves[0].ReplyTo)
	}
	if top, hasTop := rt.GetTopMsgID(); rt.ReplyToMsgID != 0 || !hasTop || top != 5 {
		t.Errorf("topic draft shape: reply_to=%d top=%d(set=%v), want reply_to=0 and top_msg_id=5", rt.ReplyToMsgID, top, hasTop)
	}
	if out.Draft == nil || out.Draft.Text != "ship it" {
		t.Fatalf("read-back draft wrong: %+v", out.Draft)
	}
	if out.Draft.ReplyToMsgID != 0 || out.Draft.ReplyToTopID != 5 {
		t.Errorf("a plain topic draft replies to nothing: reply_to=%d top=%d", out.Draft.ReplyToMsgID, out.Draft.ReplyToTopID)
	}
}

func TestSaveDraftInGeneralHasNoReplyTarget(t *testing.T) {
	h, s := learned(t)
	decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "channel:100", "topic_id", 1, "text", "hello general")))
	if _, has := s.saves[0].GetReplyTo(); has {
		t.Errorf("General is the chat's own draft slot; got reply target %+v", s.saves[0].ReplyTo)
	}
}

// Every refusal must leave Telegram untouched: no saveDraft request at all.
func TestRefusalsNeverReachSaveDraft(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		seed func(*stub)
		want string // text the model must be given
	}{
		{"forum chat without topic_id", saveArgs("chat_id", "channel:100", "text", "x"), nil, "pass topic_id"},
		{"topic_id on a chat without topics", saveArgs("chat_id", "user:1", "topic_id", 3, "text", "x"), nil, `"Alice" has no topics`},
		{"empty text without clear", saveArgs("chat_id", "me"), nil, "clear=true"},
		{"clear together with text", saveArgs("chat_id", "me", "text", "x", "clear", true), nil, "text must be empty"},
		{"text over 4096", saveArgs("chat_id", "me", "text", strings.Repeat("é", 4097)), nil, "at most 4096"},
		{"unknown chat", saveArgs("chat_id", "channel:999", "text", "x"), nil, "list_chats"},
		{"malformed chat_id", saveArgs("chat_id", "channel:abc", "text", "x"), nil, "invalid chat_id"},
		{"negative topic", saveArgs("chat_id", "me", "topic_id", -1, "text", "x"), nil, "must be positive"},
		{"existing draft, no overwrite", saveArgs("chat_id", "me", "text", "AI text"),
			func(s *stub) { s.seed(&tg.InputPeerSelf{}, 0, "user was typing this") }, "user was typing this"},
		{"clear an existing draft without overwrite", saveArgs("chat_id", "me", "clear", true),
			func(s *stub) { s.seed(&tg.InputPeerSelf{}, 0, "keep me") }, "keep me"},
		{"stale expected_draft_date", saveArgs("chat_id", "me", "text", "AI text", "expected_draft_date", "2020-01-01T00:00:00Z"),
			func(s *stub) { s.seed(&tg.InputPeerSelf{}, 0, "newer draft") }, "changed since you read it"},
		{"unparseable expected_draft_date", saveArgs("chat_id", "me", "text", "AI text", "expected_draft_date", "yesterday"),
			func(s *stub) { s.seed(&tg.InputPeerSelf{}, 0, "a draft") }, "not RFC3339"},
	}
	for _, c := range cases {
		h, s := learned(t)
		var before *tg.DraftMessage
		if c.seed != nil {
			c.seed(s)
			before = s.drafts[draftKey(&tg.InputPeerSelf{}, 0)]
		}

		res := h.call(t, "save_draft", c.args)
		if !res.IsError {
			t.Errorf("%s: want a tool error, got %s", c.name, text(res))
			continue
		}
		if !strings.Contains(text(res), c.want) {
			t.Errorf("%s: message %q should contain %q", c.name, text(res), c.want)
		}
		if n := countRPC(h, rpcSaveDraft); n != 0 || len(s.saves) != 0 {
			t.Errorf("%s: a refused call issued %d saveDraft requests", c.name, n)
		}
		if before != nil && s.drafts[draftKey(&tg.InputPeerSelf{}, 0)] != before {
			t.Errorf("%s: the user's existing draft was modified", c.name)
		}
	}
}

func TestExistingDraftIsShownAndCanBeReplacedOnPurpose(t *testing.T) {
	selfKey := &tg.InputPeerSelf{}

	// overwrite=true replaces.
	{
		h, s := learned(t)
		s.seed(selfKey, 0, "old")
		out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "text", "new", "overwrite", true)))
		if !out.Saved || out.Draft.Text != "new" || out.PreviousDraft == nil || out.PreviousDraft.Text != "old" {
			t.Errorf("overwrite: %+v", out)
		}
	}
	// A matching expected_draft_date replaces without overwrite.
	{
		h, s := learned(t)
		d := s.seed(selfKey, 0, "old")
		date := model.RFC3339(d.Date)
		out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "text", "new", "expected_draft_date", date)))
		if !out.Saved || out.Draft.Text != "new" {
			t.Errorf("compare-and-swap: %+v", out)
		}
	}
	// The refusal itself tells the model how to proceed.
	{
		h, s := learned(t)
		d := s.seed(selfKey, 0, "old")
		res := h.call(t, "save_draft", saveArgs("chat_id", "me", "text", "new"))
		if !strings.Contains(text(res), model.RFC3339(d.Date)) || !strings.Contains(text(res), "Ask the user") {
			t.Errorf("refusal should show the date and say to ask the user: %q", text(res))
		}
	}
}

func TestIdenticalAndEmptyClearsChangeNothing(t *testing.T) {
	selfKey := &tg.InputPeerSelf{}

	h, s := learned(t)
	s.seed(selfKey, 0, "same text")
	out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "text", "same text")))
	if out.Saved || countRPC(h, rpcSaveDraft) != 0 || !strings.Contains(out.Note, "identical") {
		t.Errorf("identical draft must be a no-op: %+v", out)
	}

	h, s = learned(t)
	out = decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "clear", true)))
	if out.Saved || countRPC(h, rpcSaveDraft) != 0 || len(s.saves) != 0 {
		t.Errorf("clearing nothing must be a no-op: %+v", out)
	}
}

func TestClearDeletesWhenAllowed(t *testing.T) {
	h, s := learned(t)
	s.seed(&tg.InputPeerSelf{}, 0, "to delete")
	out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "me", "clear", true, "overwrite", true)))
	if !out.Saved || out.Draft != nil || out.PreviousDraft == nil || !strings.Contains(out.Note, "deleted") {
		t.Errorf("clear result: %+v", out)
	}
	if len(s.saves) != 1 || s.saves[0].Message != "" {
		t.Errorf("clear must send an empty draft: %+v", s.saves)
	}
}

func TestSaveDraftViaUsername(t *testing.T) {
	s := &stub{resolved: &tg.ContactsResolvedPeer{
		Peer:  &tg.PeerUser{UserID: 1},
		Users: []tg.UserClass{&tg.User{ID: 1, AccessHash: 777, FirstName: "Alice", Username: "alice"}},
	}}
	h := newHarness(t, s, nil)
	out := decode[SaveDraftOut](t, h.call(t, "save_draft", saveArgs("chat_id", "@alice", "text", "hi")))

	if got, want := seq(h), "contacts.resolveUsername,messages.getPeerDialogs,messages.saveDraft,messages.getPeerDialogs"; got != want {
		t.Errorf("sequence = %s, want %s", got, want)
	}
	if ip, ok := s.saves[0].Peer.(*tg.InputPeerUser); !ok || ip.UserID != 1 || ip.AccessHash != 777 {
		t.Errorf("draft must be addressed with the resolved access hash: %+v", s.saves[0].Peer)
	}
	if out.Where != "Alice" {
		t.Errorf("where = %q", out.Where)
	}
}

func TestListTopics(t *testing.T) {
	h, s := learned(t)
	s.forumTopics = []tg.ForumTopicClass{
		&tg.ForumTopic{ID: 1, Title: "General", UnreadCount: 2},
		&tg.ForumTopic{ID: 5, Title: "Frontend", Pinned: true, Draft: &tg.DraftMessage{Message: "wip", Date: 1700000050}},
		&tg.ForumTopicDeleted{ID: 9},
	}
	out := decode[ListTopicsOut](t, h.call(t, "list_topics", map[string]any{"chat_id": "channel:100"}))

	if seq(h) != "messages.getForumTopics" {
		t.Errorf("list_topics issued %s", seq(h))
	}
	if out.Chat != "Dev Group" || len(out.Topics) != 2 || out.Count != 43 /* stub: 3 entries + 40 */ {
		t.Fatalf("got %+v", out)
	}
	if out.Topics[0].ID != 1 || out.Topics[0].Unread != 2 {
		t.Errorf("General wrong: %+v", out.Topics[0])
	}
	if d := out.Topics[1].Draft; d == nil || d.Text != "wip" || !out.Topics[1].Pinned {
		t.Errorf("topic draft must be shown so a save can be verified: %+v", out.Topics[1])
	}
}

func TestListTopicsRefusesNonForumChats(t *testing.T) {
	h, _ := learned(t)
	res := h.call(t, "list_topics", map[string]any{"chat_id": "user:1"})
	if !res.IsError || !strings.Contains(text(res), "no topics") {
		t.Errorf("want a clear refusal, got %q", text(res))
	}
	if len(h.audit) != 0 {
		t.Errorf("a non-forum chat must not reach Telegram: %v", h.audit)
	}
}

func TestReplyInsideATopicSetsBothIds(t *testing.T) {
	h, s := learned(t)
	out := decode[SaveDraftOut](t, h.call(t, "save_draft",
		saveArgs("chat_id", "channel:100", "topic_id", 5, "reply_to_msg_id", 77, "text", "answer")))

	rt := s.saves[0].ReplyTo.(*tg.InputReplyToMessage)
	if top, has := rt.GetTopMsgID(); rt.ReplyToMsgID != 77 || !has || top != 5 {
		t.Errorf("a reply inside topic 5 needs reply_to=77 and top_msg_id=5, got reply_to=%d top=%d(set=%v)", rt.ReplyToMsgID, top, has)
	}
	if out.Draft == nil || out.Draft.ReplyToMsgID != 77 {
		t.Errorf("a real reply must be reported as one: %+v", out.Draft)
	}

	// Replying to the topic root is the same as no reply.
	h, s = learned(t)
	decode[SaveDraftOut](t, h.call(t, "save_draft",
		saveArgs("chat_id", "channel:100", "topic_id", 5, "reply_to_msg_id", 5, "text", "x")))
	rt = s.saves[0].ReplyTo.(*tg.InputReplyToMessage)
	if top, has := rt.GetTopMsgID(); rt.ReplyToMsgID != 0 || !has || top != 5 {
		t.Errorf("reply to the topic root is a plain topic draft: %+v", rt)
	}
}

// Seen live: Telegram accepted a draft, the read-back found nothing, and the
// tool still said it was saved. A save is reported only if the read-back confirms it.
func TestAnUnconfirmedSaveIsNeverReportedAsSaved(t *testing.T) {
	h, s := learned(t)
	s.misplace = true

	res := h.call(t, "save_draft", saveArgs("chat_id", "channel:100", "topic_id", 5, "text", "lost"))
	if !res.IsError {
		t.Fatalf("a draft that cannot be read back must be an error, got %s", text(res))
	}
	for _, want := range []string{"did not appear", "Do NOT tell the user it is saved"} {
		if !strings.Contains(text(res), want) {
			t.Errorf("error should say %q: %q", want, text(res))
		}
	}
	if len(s.saves) != 1 {
		t.Errorf("the request itself was made once: %d", len(s.saves))
	}
}

func TestAClearThatDidNotWorkIsNotReportedAsDone(t *testing.T) {
	h, s := learned(t)
	s.seed(&tg.InputPeerSelf{}, 0, "stays")
	s.misplace = true // the clear lands elsewhere, so the draft is still there

	res := h.call(t, "save_draft", saveArgs("chat_id", "me", "clear", true, "overwrite", true))
	if !res.IsError || !strings.Contains(text(res), "still there") {
		t.Errorf("want an error saying the draft is still there, got %q", text(res))
	}
}
