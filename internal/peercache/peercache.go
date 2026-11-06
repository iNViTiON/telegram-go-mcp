// Package peercache remembers the access hashes Telegram requires to address a
// chat. It is in memory only: one owner process serves every session and lives
// as long as the app, and a miss costs the model one list_chats call.
//
// It uses telegram/message/peer for entity extraction, not the experimental
// telegram/peers package.
package peercache

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

// Entry is what is known about one chat.
type Entry struct {
	Key      string // kind-prefixed id: "user:1", "chat:2", "channel:3"
	Kind     string // user | chat | channel
	ID       int64
	Hash     int64
	Title    string
	Username string
	Forum    bool // supergroup with topics
}

// InputPeer builds the request-side peer for e.
func (e Entry) InputPeer() tg.InputPeerClass {
	switch e.Kind {
	case "user":
		return &tg.InputPeerUser{UserID: e.ID, AccessHash: e.Hash}
	case "channel":
		return &tg.InputPeerChannel{ChannelID: e.ID, AccessHash: e.Hash}
	}
	return &tg.InputPeerChat{ChatID: e.ID}
}

// Cache is safe for concurrent use.
type Cache struct {
	mu sync.RWMutex
	m  map[string]Entry
}

// New returns an empty cache.
func New() *Cache { return &Cache{m: map[string]Entry{}} }

// Learn records every full entity in ents. "Min" entities are skipped: their
// access hashes are only valid in the context they arrived in, so storing one
// could later produce a hash Telegram rejects.
func (c *Cache) Learn(ents peer.Entities) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, u := range ents.Users() {
		if u.Min {
			continue
		}
		name := strings.TrimSpace(u.FirstName + " " + u.LastName)
		if u.Deleted {
			name = "Deleted account"
		}
		c.put(Entry{Kind: "user", ID: id, Hash: u.AccessHash, Title: name, Username: u.Username})
	}
	for id, ch := range ents.Channels() {
		if ch.Min {
			continue
		}
		c.put(Entry{Kind: "channel", ID: id, Hash: ch.AccessHash, Title: ch.Title, Username: ch.Username, Forum: ch.Forum})
	}
	for id, g := range ents.Chats() {
		c.put(Entry{Kind: "chat", ID: id, Title: g.Title})
	}
}

func (c *Cache) put(e Entry) {
	e.Key = fmt.Sprintf("%s:%d", e.Kind, e.ID)
	c.m[e.Key] = e
}

// Lookup returns the entry for a kind-prefixed ref such as "channel:3".
func (c *Cache) Lookup(ref string) (Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[ref]
	return e, ok
}

// Ref is a parsed chat reference.
type Ref struct {
	Me       bool   // "me": the user's own Saved Messages
	Username string // "@name" without the "@"
	Key      string // "user:1", "chat:2" or "channel:3"
}

// ParseRef accepts "me", "@username", or a kind-prefixed id.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "me":
		return Ref{Me: true}, nil
	case strings.HasPrefix(s, "@") && len(s) > 1:
		return Ref{Username: s[1:]}, nil
	}
	kind, id, ok := strings.Cut(s, ":")
	if ok && (kind == "user" || kind == "chat" || kind == "channel") {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 {
			return Ref{Key: s}, nil
		}
	}
	return Ref{}, fmt.Errorf("invalid chat_id %q: use \"me\", \"@username\", or an id from list_chats such as \"channel:123\"", s)
}
