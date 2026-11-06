package peercache

import (
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func ents(users []tg.UserClass, chats []tg.ChatClass) peer.Entities {
	return peer.EntitiesFromResult(&tg.MessagesDialogs{Users: users, Chats: chats})
}

func TestLearnAndLookup(t *testing.T) {
	c := New()
	c.Learn(ents(
		[]tg.UserClass{&tg.User{ID: 1, AccessHash: 11, FirstName: "Alice", LastName: "A", Username: "alice"}},
		[]tg.ChatClass{
			&tg.Channel{ID: 100, AccessHash: 22, Title: "Dev", Forum: true, Username: "dev"},
			&tg.Chat{ID: 5, Title: "Family"},
		},
	))

	u, ok := c.Lookup("user:1")
	if !ok || u.Title != "Alice A" || u.Username != "alice" {
		t.Fatalf("user: %+v %v", u, ok)
	}
	if ip, ok := u.InputPeer().(*tg.InputPeerUser); !ok || ip.UserID != 1 || ip.AccessHash != 11 {
		t.Errorf("user input peer: %+v", u.InputPeer())
	}

	ch, _ := c.Lookup("channel:100")
	if !ch.Forum || ch.Title != "Dev" {
		t.Errorf("channel: %+v", ch)
	}
	if ip, ok := ch.InputPeer().(*tg.InputPeerChannel); !ok || ip.ChannelID != 100 || ip.AccessHash != 22 {
		t.Errorf("channel input peer: %+v", ch.InputPeer())
	}

	g, _ := c.Lookup("chat:5")
	if ip, ok := g.InputPeer().(*tg.InputPeerChat); !ok || ip.ChatID != 5 {
		t.Errorf("group input peer: %+v", g.InputPeer())
	}
	if _, ok := c.Lookup("channel:999"); ok {
		t.Error("unknown chat must miss")
	}
}

// A min entity's hash is only valid in the chat it arrived in. Storing it, or
// letting it replace a full entry, would later yield hashes Telegram rejects.
func TestMinEntitiesNeverStoredOrOverwrite(t *testing.T) {
	c := New()
	c.Learn(ents(nil, []tg.ChatClass{&tg.Channel{ID: 7, AccessHash: 1, Title: "Full"}}))
	c.Learn(ents([]tg.UserClass{&tg.User{ID: 2, Min: true, AccessHash: 9, FirstName: "Min"}},
		[]tg.ChatClass{&tg.Channel{ID: 7, Min: true, AccessHash: 999, Title: "Min copy"}}))

	if _, ok := c.Lookup("user:2"); ok {
		t.Error("min user must not be stored")
	}
	if e, _ := c.Lookup("channel:7"); e.Hash != 1 || e.Title != "Full" {
		t.Errorf("min channel overwrote the full entry: %+v", e)
	}
}

func TestParseRef(t *testing.T) {
	ok := map[string]Ref{
		"me":          {Me: true},
		" me ":        {Me: true},
		"@alice":      {Username: "alice"},
		"user:1":      {Key: "user:1"},
		"chat:22":     {Key: "chat:22"},
		"channel:333": {Key: "channel:333"},
	}
	for in, want := range ok {
		if got, err := ParseRef(in); err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "@", "alice", "user:", "user:abc", "user:0", "user:-1", "bot:1", "channel:1:2", "123"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) must fail", bad)
		}
	}
}
