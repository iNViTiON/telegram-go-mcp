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

// stub is the "network": it records what reaches it and answers from canned data.
type stub struct {
	dialogs func(*tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass
	users   []tg.UserClass
	lastReq *tg.MessagesGetDialogsRequest
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
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s must be annotated read-only", tool.Name)
		}
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "list_chats,whoami"; got != want {
		t.Fatalf("tools = %s, want %s", got, want)
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
