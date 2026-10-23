package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// PollVoteInput votes on a poll (MSG-23): the names of the chosen options; an empty list
// removes the vote.
type PollVoteInput struct {
	Options         []string `json:"options"`
	ClientMessageID string   `json:"client_message_id,omitempty"`
}

// VotePoll votes on a poll of the chat (ours or a contact's), replacing our previous vote.
// It goes through the send queue; the vote is encrypted with the poll's key, so polls
// received before this number was paired cannot be voted on. The poll's votes and
// results (GET /messages/{id}) count our vote once, as "me".
func (s *Messaging) VotePoll(ctx context.Context, instanceID, id string, in PollVoteInput, wait time.Duration) (MessageView, error) {
	if in.Options == nil {
		return MessageView{}, apierr.Validation("options is required (an empty list removes the vote)")
	}
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessageView{}, mapErr(err)
	}
	m, err := s.Store.GetMessage(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, notFound("message")
	}
	if err != nil {
		return MessageView{}, err
	}
	poll := store.ParseMeta(m.MetaJSON).Poll
	if m.Type != "poll" || poll == nil {
		return MessageView{}, apierr.Validation("the message is not a poll")
	}
	if !m.RevokedAt.IsZero() {
		return MessageView{}, apierr.Validation("the poll was deleted")
	}
	if m.Status == "pending" || m.WAMessageID == "" {
		return MessageView{}, apierr.Validation("the poll has not been sent yet")
	}
	seen := map[string]bool{}
	for _, o := range in.Options {
		if !slices.Contains(poll.Options, o) {
			return MessageView{}, apierr.Validation("options must be names of the poll's options: " + o + " is not one")
		}
		if seen[o] {
			return MessageView{}, apierr.Validation("options must be distinct")
		}
		seen[o] = true
	}
	if poll.Selectable > 0 && len(in.Options) > poll.Selectable {
		return MessageView{}, apierr.Validation("this poll allows choosing at most " + strconv.Itoa(poll.Selectable) + " option(s)")
	}
	v := in
	return s.send(ctx, instanceID, SendInput{Type: "poll_vote", MessageID: m.ID, ClientMessageID: in.ClientMessageID,
		pollVote: &v}, m.ChatJID, wait)
}
