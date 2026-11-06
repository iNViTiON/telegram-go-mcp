package tools

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/gotd/td/tg"
)

// cursor is the opaque position of a paginated getDialogs listing. It carries
// the offset peer's access hash, so paging works after a restart without a
// persistent peer cache.
type cursor struct {
	Date int    `json:"d"`
	ID   int    `json:"m"`
	Kind string `json:"k"` // user | chat | channel
	Peer int64  `json:"p"`
	Hash int64  `json:"h,omitempty"`
}

var errBadCursor = errors.New("invalid cursor: pass next_cursor exactly as returned by the previous call")

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return cursor{}, errBadCursor
	}
	if c.Date <= 0 || c.ID <= 0 || c.Peer <= 0 {
		return cursor{}, errBadCursor
	}
	switch c.Kind {
	case "user", "chat", "channel":
		return c, nil
	}
	return cursor{}, errBadCursor
}

func (c cursor) inputPeer() tg.InputPeerClass {
	switch c.Kind {
	case "user":
		return &tg.InputPeerUser{UserID: c.Peer, AccessHash: c.Hash}
	case "channel":
		return &tg.InputPeerChannel{ChannelID: c.Peer, AccessHash: c.Hash}
	}
	return &tg.InputPeerChat{ChatID: c.Peer}
}

func cursorFor(date, id int, p tg.InputPeerClass) (cursor, bool) {
	c := cursor{Date: date, ID: id}
	switch p := p.(type) {
	case *tg.InputPeerUser:
		c.Kind, c.Peer, c.Hash = "user", p.UserID, p.AccessHash
	case *tg.InputPeerChannel:
		c.Kind, c.Peer, c.Hash = "channel", p.ChannelID, p.AccessHash
	case *tg.InputPeerChat:
		c.Kind, c.Peer = "chat", p.ChatID
	default:
		return cursor{}, false
	}
	return c, c.Date > 0 && c.ID > 0
}
