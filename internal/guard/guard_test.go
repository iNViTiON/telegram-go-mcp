package guard_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/hisoft/telegram-go-mcp/internal/guard"
	"github.com/hisoft/telegram-go-mcp/internal/tgclient"
)

// wantRead is a SECOND, literal copy of the read policy. It is duplicated on
// purpose: changing guard.go fails these tests until this list is edited too,
// so widening the policy always takes two deliberate edits.
var wantRead = map[string]bool{
	"UsersGetUsersRequest":                true,
	"MessagesGetDialogsRequest":           true,
	"MessagesGetPeerDialogsRequest":       true,
	"MessagesGetHistoryRequest":           true,
	"MessagesGetRepliesRequest":           true,
	"MessagesGetMessagesRequest":          true,
	"ChannelsGetMessagesRequest":          true,
	"MessagesSearchRequest":               true,
	"MessagesGetForumTopicsRequest":       true,
	"MessagesGetForumTopicsByIDRequest":   true,
	"MessagesGetAllDraftsRequest":         true,
	"MessagesGetDiscussionMessageRequest": true,
	"ContactsResolveUsernameRequest":      true,
	"ChannelsGetChannelsRequest":          true,
	"MessagesSaveDraftRequest":            true, // the only write
}

var wantLogin = map[string]bool{
	"AuthSendCodeRequest":       true,
	"AuthResendCodeRequest":     true,
	"AuthSignInRequest":         true,
	"AuthCheckPasswordRequest":  true,
	"AccountGetPasswordRequest": true,
	"UsersGetUsersRequest":      true,
}

// newReadClient builds the production client. It never calls Run, so allowed
// requests park inside gotd waiting for a session: that is the "would have hit
// the wire" signal, with no network involved.
func newReadClient(t *testing.T) *tgclient.Client {
	t.Helper()
	return tgclient.New(tgclient.Config{
		AppID:       1,
		AppHash:     "test",
		SessionPath: filepath.Join(t.TempDir(), "session.json"),
		Policy:      guard.PolicyRead,
	})
}

func TestSendDeniedThroughRealClient(t *testing.T) {
	c := newReadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "must never leave",
		RandomID: 1,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, guard.ErrDenied) {
		t.Fatalf("want ErrDenied, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("send must be rejected, not parked until timeout")
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("denial took %v, want immediate", elapsed)
	}
}

func TestAllowedReachesTransport(t *testing.T) {
	c := newReadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		Limit:      1,
		OffsetPeer: &tg.InputPeerEmpty{},
	})

	// Proves the guard sits in the real chain without shadowing everything: an
	// allowed request must get past it and park in the (unconnected) transport.
	if errors.Is(err, guard.ErrDenied) {
		t.Fatalf("getDialogs must be allowed, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want request to park until deadline, got %v", err)
	}
}

func TestWrapperSmuggling(t *testing.T) {
	c := newReadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	send := &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "x", RandomID: 1}
	cases := map[string]bin.Encoder{
		"invokeWithLayer": &tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: send},
		"initConnection":  &tg.InitConnectionRequest{APIID: 1, Query: send},
	}
	for name, req := range cases {
		var out tg.UpdatesBox
		err := c.Invoke(ctx, req, &out)
		if !errors.Is(err, guard.ErrDenied) {
			t.Errorf("%s: want ErrDenied, got %v", name, err)
		}
	}
}

// requestTypes returns every TL method type in the registry, keyed by Go name.
func requestTypes(t *testing.T) map[string]bin.Object {
	t.Helper()
	out := map[string]bin.Object{}
	for _, ctor := range tg.TypesConstructorMap() {
		o := ctor()
		rt := reflect.TypeOf(o)
		if rt.Kind() != reflect.Ptr {
			continue
		}
		if name := rt.Elem().Name(); strings.HasSuffix(name, "Request") {
			out[name] = o
		}
	}
	if len(out) < 500 { // 868 at gotd v0.163.0; the floor only catches a broken walk
		t.Fatalf("registry looks wrong: only %d request types", len(out))
	}
	return out
}

func TestDenyByDefaultExhaustive(t *testing.T) {
	reqs := requestTypes(t)

	// A typo in the literal lists must fail loudly, not silently allow nothing.
	for _, want := range []map[string]bool{wantRead, wantLogin} {
		for name := range want {
			if _, ok := reqs[name]; !ok {
				t.Errorf("test list names %q, which is not in the TL registry", name)
			}
		}
	}

	for name, o := range reqs {
		enc, ok := o.(bin.Encoder)
		if !ok {
			t.Fatalf("%s is not a bin.Encoder", name)
		}
		if got := guard.Allowed(guard.PolicyRead, enc); got != wantRead[name] {
			t.Errorf("PolicyRead %s: allowed=%v, want %v", name, got, wantRead[name])
		}
		if got := guard.Allowed(guard.PolicyLogin, enc); got != wantLogin[name] {
			t.Errorf("PolicyLogin %s: allowed=%v, want %v", name, got, wantLogin[name])
		}
	}
}

var writeLooking = regexp.MustCompile(`(?i)(^(auth|account)\.|send|forward|delete|edit|readhistory|readdiscussion|invokewith|initconnection|toggle|create|join|leave|import|export|\.set)`)

func TestNoWriteNames(t *testing.T) {
	var offenders []string
	for name, o := range requestTypes(t) {
		enc := o.(bin.Encoder)
		if !guard.Allowed(guard.PolicyRead, enc) {
			continue
		}
		method := guard.MethodName(enc)
		if method == "messages.saveDraft" || strings.HasPrefix(method, "messages.saveDraft#") {
			continue // the single named exemption
		}
		if writeLooking.MatchString(method) {
			offenders = append(offenders, name+" ("+method+")")
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("PolicyRead allows write-looking methods: %v", offenders)
	}
}

func TestTypeIDsPinned(t *testing.T) {
	// If a gotd bump re-points a Go type at a different TL method, this breaks
	// instead of silently changing what the guard permits. IDs come from the
	// schema in the pinned module (_schema/telegram.tl).
	pins := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"messages.saveDraft", tg.MessagesSaveDraftRequestTypeID, 0xad0fa15c},
		{"messages.sendMessage", tg.MessagesSendMessageRequestTypeID, 0xfef48f62},
		{"messages.forwardMessages", tg.MessagesForwardMessagesRequestTypeID, 0x13704a7c},
		{"messages.deleteMessages", tg.MessagesDeleteMessagesRequestTypeID, 0xe58e95d2},
		{"messages.readHistory", tg.MessagesReadHistoryRequestTypeID, 0x0e306d3a},
	}
	for _, p := range pins {
		if p.got != p.want {
			t.Errorf("%s: type id %#x, want %#x", p.name, p.got, p.want)
		}
	}
}

func TestSaveDraftShapeRejected(t *testing.T) {
	text := func() *tg.MessagesSaveDraftRequest {
		return &tg.MessagesSaveDraftRequest{Peer: &tg.InputPeerSelf{}, Message: "hi"}
	}
	if !guard.Allowed(guard.PolicyRead, text()) {
		t.Fatal("plain text draft must be allowed")
	}

	withTopic := text()
	withTopic.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: 1, TopMsgID: 1})
	if !guard.Allowed(guard.PolicyRead, withTopic) {
		t.Fatal("draft replying to a message in a topic must be allowed")
	}

	bad := map[string]*tg.MessagesSaveDraftRequest{}
	m := text()
	m.SetMedia(&tg.InputMediaEmpty{})
	bad["media"] = m
	e := text()
	e.SetEffect(5)
	bad["effect"] = e
	s := text()
	s.SetSuggestedPost(tg.SuggestedPost{})
	// SuggestedPost{} is the zero value; set a real field so it is non-zero.
	s.SuggestedPost.SetPrice(&tg.StarsAmount{Amount: 1})
	bad["suggested_post"] = s
	r := text()
	r.SetReplyTo(&tg.InputReplyToStory{})
	bad["reply_to_story"] = r

	for name, req := range bad {
		if guard.Allowed(guard.PolicyRead, req) {
			t.Errorf("%s draft must be denied", name)
		}
	}
}

func TestLoginPolicyHasNoContentReads(t *testing.T) {
	for _, req := range []bin.Encoder{
		&tg.MessagesGetHistoryRequest{},
		&tg.MessagesGetDialogsRequest{},
		&tg.MessagesSaveDraftRequest{},
		&tg.MessagesSearchRequest{},
	} {
		if guard.Allowed(guard.PolicyLogin, req) {
			t.Errorf("PolicyLogin must deny %s", guard.MethodName(req))
		}
	}
}

func TestReadPolicyHasNoAuth(t *testing.T) {
	for _, req := range []bin.Encoder{
		&tg.AuthSendCodeRequest{},
		&tg.AuthSignInRequest{},
		&tg.AuthLogOutRequest{},
		&tg.AccountGetPasswordRequest{},
	} {
		if guard.Allowed(guard.PolicyRead, req) {
			t.Errorf("PolicyRead must deny %s", guard.MethodName(req))
		}
	}
}

func TestAuthDeniedThroughRealReadClient(t *testing.T) {
	c := newReadClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.API().AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+10000000000", APIID: 1, APIHash: "x"})
	if !errors.Is(err, guard.ErrDenied) {
		t.Fatalf("server client must not be able to start a login, got %v", err)
	}
}
