package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
)

var errStub = errors.New("stub transport: no canned answer")

// stub is the "network": it records what reaches it and answers from canned
// data. Drafts are stateful, keyed by chat and topic, so read-back, conflicts
// and compare-and-swap behave like the real thing.
type stub struct {
	dialogs func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass
	users   []tg.UserClass
	lastReq *tg.MessagesGetDialogsRequest

	forumPeers  map[string]bool // peers that are forum supergroups
	drafts      map[string]*tg.DraftMessage
	topicTitles map[int]string
	forumTopics []tg.ForumTopicClass
	resolved    *tg.ContactsResolvedPeer
	saves       []*tg.MessagesSaveDraftRequest
	date        int
}

func peerKey(p tg.InputPeerClass) string {
	switch p := p.(type) {
	case *tg.InputPeerSelf:
		return "self"
	case *tg.InputPeerUser:
		return fmt.Sprintf("user:%d", p.UserID)
	case *tg.InputPeerChannel:
		return fmt.Sprintf("channel:%d", p.ChannelID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("chat:%d", p.ChatID)
	}
	return "?"
}

// General (topic 1) and a plain chat share one draft slot, as assumed in drafts.go.
func draftKey(p tg.InputPeerClass, topic int) string {
	if topic == 1 {
		topic = 0
	}
	return fmt.Sprintf("%s/%d", peerKey(p), topic)
}

func (s *stub) seed(p tg.InputPeerClass, topic int, text string) *tg.DraftMessage {
	if s.drafts == nil {
		s.drafts = map[string]*tg.DraftMessage{}
	}
	s.date += 10
	d := &tg.DraftMessage{Message: text, Date: 1700000000 + s.date}
	s.drafts[draftKey(p, topic)] = d
	return d
}

func (s *stub) draftFor(p tg.InputPeerClass, topic int) tg.DraftMessageClass {
	if d := s.drafts[draftKey(p, topic)]; d != nil {
		return d
	}
	return nil // a true nil interface, not a typed nil
}

func (s *stub) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	switch r := in.(type) {
	case *tg.MessagesGetDialogsRequest:
		s.lastReq = r
		if s.dialogs != nil {
			out.(*tg.MessagesDialogsBox).Dialogs = s.dialogs(r)
			return nil
		}
	case *tg.UsersGetUsersRequest:
		if s.users != nil {
			out.(*tg.UserClassVector).Elems = s.users
			return nil
		}
	case *tg.MessagesGetPeerDialogsRequest:
		p := r.Peers[0].(*tg.InputDialogPeer).Peer
		*out.(*tg.MessagesPeerDialogs) = tg.MessagesPeerDialogs{
			Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, Draft: s.draftFor(p, 0)}},
		}
		return nil
	case *tg.MessagesGetForumTopicsByIDRequest:
		id := r.Topics[0]
		title := s.topicTitles[id]
		if title == "" {
			title = fmt.Sprintf("Topic %d", id)
		}
		*out.(*tg.MessagesForumTopics) = tg.MessagesForumTopics{
			Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: id, Title: title, Draft: s.draftFor(r.Peer, id)}},
		}
		return nil
	case *tg.MessagesGetForumTopicsRequest:
		*out.(*tg.MessagesForumTopics) = tg.MessagesForumTopics{Count: len(s.forumTopics) + 40, Topics: s.forumTopics}
		return nil
	case *tg.ContactsResolveUsernameRequest:
		if s.resolved != nil {
			*out.(*tg.ContactsResolvedPeer) = *s.resolved
			return nil
		}
	case *tg.MessagesSaveDraftRequest:
		s.saves = append(s.saves, r)
		// Per core.telegram.org/api/forum: in a forum, reply_to_msg_id alone
		// addresses that topic; top_msg_id is only set for a reply to some other
		// message inside a topic.
		topic := 0
		if rt, ok := r.ReplyTo.(*tg.InputReplyToMessage); ok {
			if top, ok := rt.GetTopMsgID(); ok {
				topic = top
			} else if s.forumPeers[peerKey(r.Peer)] {
				topic = rt.ReplyToMsgID
			}
		}
		if s.drafts == nil {
			s.drafts = map[string]*tg.DraftMessage{}
		}
		if r.Message == "" {
			delete(s.drafts, draftKey(r.Peer, topic))
		} else {
			d := s.seed(r.Peer, topic, r.Message)
			if rt, ok := r.GetReplyTo(); ok {
				d.SetReplyTo(rt) // Telegram stores the reply target with the draft
			}
		}
		out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
		return nil
	}
	return errStub
}

// harness wires the tools to the REAL guard middleware over the stub, and
// records every method name the guard sees.
type harness struct {
	cs     *mcp.ClientSession
	stub   *stub
	audit  []string
	denied []string
	ready  func(context.Context) error
}

func newHarness(t *testing.T, s *stub, ready func(context.Context) error) *harness {
	t.Helper()
	h := &harness{stub: s, ready: ready}

	inv := guard.Middleware(guard.PolicyRead, func(method string, allowed bool) {
		name, _, _ := strings.Cut(method, "#")
		h.audit = append(h.audit, name)
		if !allowed {
			h.denied = append(h.denied, name)
		}
	}).Handle(s)

	srv := NewServer(Deps{API: tg.NewClient(inv), Ready: ready}, "test")
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	h.cs = cs
	return h
}

func (h *harness) call(t *testing.T, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := h.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestToolSurface(t *testing.T) {
	h := newHarness(t, &stub{}, nil)
	res, err := h.cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names, writers []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			writers = append(writers, tool.Name)
		}
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "list_chats,list_topics,save_draft,whoami"; got != want {
		t.Fatalf("tools = %s, want %s", got, want)
	}
	// Exactly one tool may be non-read-only, and it must be the draft saver.
	if len(writers) != 1 || writers[0] != "save_draft" {
		t.Fatalf("non-read-only tools = %v, want exactly [save_draft]", writers)
	}
}

// The plan's tool -> RPC table, as a tested fact. Every RPC a tool issues must
// pass the real guard, and nothing else may be issued.
func TestEachToolIssuesOnlyItsRPCs(t *testing.T) {
	cases := []struct {
		tool string
		want []string
	}{
		{"whoami", []string{"users.getUsers"}},
		{"list_chats", []string{"messages.getDialogs"}},
	}
	for _, c := range cases {
		h := newHarness(t, &stub{}, nil) // stub answers errStub: the tool errors, which is fine here
		h.call(t, c.tool, map[string]any{})
		if got := strings.Join(h.audit, ","); got != strings.Join(c.want, ",") {
			t.Errorf("%s issued %v, want %v", c.tool, h.audit, c.want)
		}
		if len(h.denied) != 0 {
			t.Errorf("%s issued RPCs the guard denies: %v", c.tool, h.denied)
		}
	}
}

func TestNotReadyIsAToolErrorNotAProtocolError(t *testing.T) {
	notReady := func(context.Context) error { return fmt.Errorf("waiting: %w", tgclient.ErrNotReady) }
	h := newHarness(t, &stub{}, notReady)

	for _, tool := range []string{"whoami", "list_chats"} {
		res := h.call(t, tool, map[string]any{})
		if !res.IsError {
			t.Errorf("%s: want IsError", tool)
		}
		if !strings.Contains(text(res), "not ready") {
			t.Errorf("%s: model should be told the connection is not ready, got %q", tool, text(res))
		}
	}
	if len(h.audit) != 0 {
		t.Errorf("no RPC may be issued before the connection is ready, saw %v", h.audit)
	}
}

func TestWhoamiOmitsPhone(t *testing.T) {
	s := &stub{users: []tg.UserClass{&tg.User{ID: 42, FirstName: "Mix", LastName: "Test", Username: "mixtest", Phone: "+10000000000"}}}
	h := newHarness(t, s, nil)

	res := h.call(t, "whoami", map[string]any{})
	out := decode[WhoamiOut](t, res)
	if out.ID != "user:42" || out.Name != "Mix Test" || out.Username != "mixtest" {
		t.Fatalf("got %+v", out)
	}
	if strings.Contains(text(res), "10000000000") {
		t.Fatal("phone number must never be returned")
	}
}

func msg(id int, peerID tg.PeerClass, date int, body string, out bool, from tg.PeerClass) *tg.Message {
	m := &tg.Message{ID: id, PeerID: peerID, Date: date, Message: body, Out: out}
	if from != nil {
		m.SetFromID(from)
	}
	return m
}

func dialogsFixture() *tg.MessagesDialogsSlice {
	long := strings.Repeat("x", 500)
	return &tg.MessagesDialogsSlice{
		Count: 99,
		Dialogs: []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 100}, TopMessage: 10, UnreadCount: 3, UnreadMentionsCount: 1, Pinned: true},
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 20},
		},
		Messages: []tg.MessageClass{
			msg(10, &tg.PeerChannel{ChannelID: 100}, 1700000000, "hello\n  world "+long, false, &tg.PeerUser{UserID: 2}),
			msg(20, &tg.PeerUser{UserID: 1}, 1700000100, "hi", true, nil),
		},
		Chats: []tg.ChatClass{
			&tg.Channel{ID: 100, AccessHash: 555, Title: "Dev Group", Megagroup: true, Forum: true, Username: "devgroup"},
		},
		Users: []tg.UserClass{
			&tg.User{ID: 1, AccessHash: 777, FirstName: "Alice", Username: "alice"},
			&tg.User{ID: 2, AccessHash: 888, FirstName: "Bob", LastName: "B"},
		},
	}
}

func TestListChatsShapeAndPagination(t *testing.T) {
	s := &stub{dialogs: func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass { return dialogsFixture() }}
	h := newHarness(t, s, nil)

	out := decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{"limit": 2}))
	if len(out.Chats) != 2 {
		t.Fatalf("got %d chats", len(out.Chats))
	}

	forum := out.Chats[0]
	if forum.ID != "channel:100" || forum.Kind != "supergroup" || !forum.Forum || forum.Title != "Dev Group" || forum.Username != "devgroup" {
		t.Errorf("forum chat wrong: %+v", forum)
	}
	if forum.Unread != 3 || forum.UnreadMentions != 1 || !forum.Pinned {
		t.Errorf("forum counters wrong: %+v", forum)
	}
	lm := forum.LastMessage
	if lm == nil || lm.ID != 10 || lm.Date != "2023-11-14T22:13:20Z" || lm.From != "Bob B" {
		t.Fatalf("last message wrong: %+v", lm)
	}
	if strings.ContainsAny(lm.Text, "\n") || !strings.HasPrefix(lm.Text, "hello world ") || len([]rune(lm.Text)) > 201 {
		t.Errorf("snippet not collapsed/capped: %q (%d runes)", lm.Text, len([]rune(lm.Text)))
	}

	dm := out.Chats[1]
	if dm.ID != "user:1" || dm.Kind != "user" || dm.Title != "Alice" || dm.LastMessage == nil || !dm.LastMessage.Out || dm.LastMessage.From != "me" {
		t.Errorf("dm wrong: %+v / %+v", dm, dm.LastMessage)
	}

	if out.NextCursor == "" {
		t.Fatal("a full page of a slice result must offer a next_cursor")
	}
	if s.lastReq.ExcludePinned {
		t.Error("the first page must include pinned chats")
	}
	// Second page: the request must carry offsets derived from the LAST dialog.
	decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{"limit": 2, "cursor": out.NextCursor}))
	r := s.lastReq
	if !r.ExcludePinned {
		t.Error("later pages must set exclude_pinned: Telegram otherwise repeats pinned chats (seen live)")
	}
	peer, ok := r.OffsetPeer.(*tg.InputPeerUser)
	if r.OffsetID != 20 || r.OffsetDate != 1700000100 || !ok || peer.UserID != 1 || peer.AccessHash != 777 {
		t.Fatalf("offsets not taken from the last dialog: %+v peer=%+v", r, r.OffsetPeer)
	}
}

func TestListChatsClampsLimitAndRejectsBadCursor(t *testing.T) {
	s := &stub{dialogs: func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass {
		return &tg.MessagesDialogs{}
	}}
	h := newHarness(t, s, nil)

	decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{"limit": 100000}))
	if s.lastReq.Limit != maxChatLimit {
		t.Errorf("limit not clamped: %d", s.lastReq.Limit)
	}
	decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{}))
	if s.lastReq.Limit != defaultChatLimit {
		t.Errorf("default limit wrong: %d", s.lastReq.Limit)
	}

	before := len(h.audit)
	res := h.call(t, "list_chats", map[string]any{"cursor": "not-a-cursor"})
	if !res.IsError || !strings.Contains(text(res), "invalid cursor") {
		t.Errorf("bad cursor must be a clear tool error, got %q", text(res))
	}
	if len(h.audit) != before {
		t.Error("a bad cursor must not reach Telegram")
	}
}

func TestListChatsLastPageHasNoCursor(t *testing.T) {
	s := &stub{dialogs: func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass {
		f := dialogsFixture()
		return &tg.MessagesDialogs{Dialogs: f.Dialogs, Messages: f.Messages, Chats: f.Chats, Users: f.Users}
	}}
	h := newHarness(t, s, nil)
	out := decode[ListChatsOut](t, h.call(t, "list_chats", map[string]any{"limit": 2}))
	if out.NextCursor != "" {
		t.Errorf("non-slice result is complete; got cursor %q", out.NextCursor)
	}
}

func TestCursorRoundTripAndRejects(t *testing.T) {
	c, ok := cursorFor(1700000000, 5, &tg.InputPeerChannel{ChannelID: 9, AccessHash: 3})
	if !ok {
		t.Fatal("cursorFor failed")
	}
	got, err := decodeCursor(encodeCursor(c))
	if err != nil || got != c {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for _, bad := range []string{"", "!!!", "e30", encodeCursor(cursor{Date: 1, ID: 1, Kind: "evil", Peer: 1})} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q) must fail", bad)
		}
	}
}
