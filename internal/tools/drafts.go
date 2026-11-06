package tools

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hisoft/telegram-go-mcp/internal/model"
	"github.com/hisoft/telegram-go-mcp/internal/tgerrs"
)

const maxDraftRunes = 4096 // Telegram's message length limit

// SaveDraftIn are the save_draft arguments.
type SaveDraftIn struct {
	ChatID            string `json:"chat_id" jsonschema:"\"me\" for Saved Messages, \"@username\", or an id from list_chats such as user:1 or channel:123"`
	Text              string `json:"text,omitempty" jsonschema:"the draft text, up to 4096 characters; empty only together with clear"`
	TopicID           int    `json:"topic_id,omitempty" jsonschema:"required for forum supergroups (1 is General); from list_topics"`
	ReplyToMsgID      int    `json:"reply_to_msg_id,omitempty" jsonschema:"make the draft a reply to this message id"`
	Overwrite         bool   `json:"overwrite,omitempty" jsonschema:"replace an existing draft. Only after the user agreed: it deletes what they were typing"`
	ExpectedDraftDate string `json:"expected_draft_date,omitempty" jsonschema:"the date of the existing draft you saw, to replace exactly that draft and nothing newer"`
	Clear             bool   `json:"clear,omitempty" jsonschema:"delete the draft instead of saving one; text must be empty"`
}

// SaveDraftOut is the save_draft result.
type SaveDraftOut struct {
	Saved         bool         `json:"saved"`
	Where         string       `json:"where"` // where the user should look
	Draft         *model.Draft `json:"draft,omitempty"`
	PreviousDraft *model.Draft `json:"previous_draft,omitempty"`
	Note          string       `json:"note"`
}

// saveDraft issues, in order: [contacts.resolveUsername for an @username], then
// messages.getPeerDialogs (chat) or messages.getForumTopicsByID (topic) to read
// the current draft, then messages.saveDraft, then the same read again to
// confirm. Every refusal happens before the saveDraft step.
func (t *Tools) saveDraft(ctx context.Context, _ *mcp.CallToolRequest, in SaveDraftIn) (*mcp.CallToolResult, SaveDraftOut, error) {
	var out SaveDraftOut
	if err := t.ready(ctx); err != nil {
		return nil, out, err
	}

	// Refuse what needs no Telegram call at all.
	switch {
	case in.Clear && in.Text != "":
		return nil, out, errors.New("clear deletes the draft, so text must be empty")
	case !in.Clear && in.Text == "":
		return nil, out, errors.New("text is empty. To delete the existing draft pass clear=true (and overwrite=true after the user agreed)")
	case utf8.RuneCountInString(in.Text) > maxDraftRunes:
		return nil, out, fmt.Errorf("text is %d characters; Telegram allows at most %d", utf8.RuneCountInString(in.Text), maxDraftRunes)
	case in.TopicID < 0 || in.ReplyToMsgID < 0:
		return nil, out, errors.New("topic_id and reply_to_msg_id must be positive")
	}

	c, err := t.resolve(ctx, in.ChatID)
	if err != nil {
		return nil, out, err
	}
	// A guessed placement is the worst outcome, so never guess.
	if c.Forum && in.TopicID == 0 {
		return nil, out, fmt.Errorf("%s has topics: pass topic_id (1 is General). Use list_topics to see them", c.Title)
	}
	if !c.Forum && in.TopicID != 0 {
		return nil, out, fmt.Errorf("%s has no topics, so topic_id does not apply", c.Title)
	}

	cur, topicTitle, err := t.readDraft(ctx, c, in.TopicID)
	if err != nil {
		return nil, out, err
	}
	out.Where = c.Title
	if topicTitle != "" {
		out.Where += " › " + topicTitle
	}
	out.PreviousDraft = model.DraftOf(cur)
	prev := out.PreviousDraft

	// Decide whether this write may happen. Nothing below this block can
	// widen it: the checks run first and return before saveDraft.
	switch {
	case in.Clear && prev == nil:
		out.Note = "There is no draft to clear in " + out.Where + ". Nothing changed."
		return nil, out, nil
	case !in.Clear && prev != nil && prev.Text == in.Text:
		out.Draft = prev
		out.Note = "An identical draft is already in " + out.Where + ". Nothing changed."
		return nil, out, nil
	case prev != nil:
		if err := checkMayReplace(prev, in, out.Where); err != nil {
			return nil, out, err
		}
	}

	req := &tg.MessagesSaveDraftRequest{Peer: c.Input, Message: in.Text}
	if rt := replyTo(in.TopicID, in.ReplyToMsgID); rt != nil {
		req.SetReplyTo(rt)
	}
	if _, err := t.API.MessagesSaveDraft(ctx, req); err != nil {
		return nil, out, tgerrs.Error(err)
	}

	// Read it back: the stored draft is the proof, and its date is the
	// compare-and-swap token for the next call.
	after, _, err := t.readDraft(ctx, c, in.TopicID)
	if err != nil {
		return nil, out, err
	}
	out.Draft = model.DraftOf(after)

	// Never claim success the read-back does not confirm. A request Telegram
	// accepted can still be stored somewhere other than where it was checked.
	switch {
	case in.Clear && out.Draft != nil:
		return nil, out, fmt.Errorf("asked Telegram to delete the draft in %s but it is still there: %q. Tell the user it was not deleted", out.Where, out.Draft.Text)
	case !in.Clear && (out.Draft == nil || out.Draft.Text != in.Text):
		return nil, out, fmt.Errorf("Telegram accepted the draft but it did not appear in %s when read back, so it may have been stored somewhere else "+
			"(for example as a chat-level draft). Do NOT tell the user it is saved there. Ask them to look in that chat, and to delete any stray draft", out.Where)
	}
	out.Saved = true
	if in.Clear {
		out.Note = "Draft deleted from " + out.Where + "."
	} else {
		out.Note = "Saved as a draft. Nothing was sent. The user can review and send it from the Telegram app, in " + out.Where + "."
	}
	return nil, out, nil
}

// checkMayReplace refuses to replace a draft the user may still be working on,
// unless the caller names that exact draft or was told to overwrite.
func checkMayReplace(prev *model.Draft, in SaveDraftIn, where string) error {
	if in.ExpectedDraftDate != "" {
		want, err := time.Parse(time.RFC3339, in.ExpectedDraftDate)
		if err != nil {
			return fmt.Errorf("expected_draft_date %q is not RFC3339; pass the date exactly as returned", in.ExpectedDraftDate)
		}
		have, _ := time.Parse(time.RFC3339, prev.Date)
		if want.Unix() != have.Unix() {
			return fmt.Errorf("the draft in %s changed since you read it (now dated %s): %q. Show it to the user before replacing it", where, prev.Date, prev.Text)
		}
		return nil
	}
	if in.Overwrite {
		return nil
	}
	return fmt.Errorf("%s already has a draft the user may be typing (dated %s): %q. "+
		"Nothing was changed. Ask the user; to replace exactly this draft call again with expected_draft_date=%q, "+
		"or with overwrite=true if they agree to lose it", where, prev.Date, prev.Text, prev.Date)
}

// replyTo builds the draft's reply target.
//
// A plain draft in a topic other than General is reply_to_msg_id = 0 with
// top_msg_id = the topic id. That is exactly what the official app writes: read
// back from drafts typed by hand in two topics, both held an InputReplyToMessage
// with reply_to_msg_id 0 and top_msg_id set to the topic. Nothing is replied to,
// which is why no reply bar is shown.
//
// Two other shapes were tried live and are WRONG for drafts:
//   - reply_to = topic and top_msg_id = topic: lands in the topic but displays as a
//     reply to the topic's first message.
//   - reply_to = topic with no top_msg_id (what the sending docs suggest): saved
//     without error and then appeared in no topic.
//
// A real reply inside a topic sets both ids. Replying to the topic root is the
// same as no reply.
func replyTo(topicID, replyToMsgID int) tg.InputReplyToClass {
	switch {
	case topicID > 1:
		rt := &tg.InputReplyToMessage{}
		rt.SetTopMsgID(topicID)
		if replyToMsgID != 0 && replyToMsgID != topicID {
			rt.ReplyToMsgID = replyToMsgID
		}
		return rt
	case replyToMsgID != 0:
		return &tg.InputReplyToMessage{ReplyToMsgID: replyToMsgID}
	}
	return nil // a plain draft, or General
}

// readDraft returns the current draft and, for topics, the topic's title.
//
// readDraft issues: messages.getForumTopicsByID (topic) or messages.getPeerDialogs (chat).
func (t *Tools) readDraft(ctx context.Context, c chat, topicID int) (tg.DraftMessageClass, string, error) {
	if topicID > 0 {
		res, err := t.API.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: c.Input, Topics: []int{topicID}})
		if err != nil {
			return nil, "", tgerrs.Error(err)
		}
		for _, tp := range res.Topics {
			if tp, ok := tp.(*tg.ForumTopic); ok && tp.ID == topicID {
				return tp.Draft, tp.Title, nil
			}
		}
		return nil, "", fmt.Errorf("topic %d does not exist in %s. Use list_topics", topicID, c.Title)
	}

	res, err := t.API.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: c.Input}})
	if err != nil {
		return nil, "", tgerrs.Error(err)
	}
	for _, d := range res.Dialogs {
		if d, ok := d.(*tg.Dialog); ok {
			return d.Draft, "", nil
		}
	}
	return nil, "", nil
}
