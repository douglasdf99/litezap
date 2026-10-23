package worker

import (
	"context"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// pollSecret finds the secret of a poll (ours or a contact's) and the sender it was
// stored with, like eventSecret.
func (w *Whatsmeow) pollSecret(ctx context.Context, chat, author types.JID, id string) ([]byte, types.JID, error) {
	if w.cli == nil || w.cli.Store == nil || w.cli.Store.MsgSecrets == nil {
		return nil, types.EmptyJID, grpcErr(codes.Unknown, "worker_error", "message secret store unavailable")
	}
	secret, stored, err := w.lookupSecret(ctx, chat, w.identities(ctx, author, types.EmptyJID), id)
	if err != nil {
		return nil, types.EmptyJID, grpcErr(codes.Unknown, "worker_error", "cannot read the poll's key")
	}
	if secret == nil {
		return nil, types.EmptyJID, grpcErr(codes.FailedPrecondition, "poll_secret_missing",
			"the poll's key is not known to this session (polls received before pairing cannot be voted on)")
	}
	return secret, stored, nil
}

// buildPollVote votes on a poll (ours or a contact's) with the options in req.Poll (none:
// remove the vote), encrypted as whatsmeow's BuildPollVote does, but with the secret found
// under every identity of the poll's author (phone number or LID; see openSecret).
func (w *Whatsmeow) buildPollVote(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	if req.TargetId == "" {
		return nil, invalid("a poll vote needs the poll")
	}
	author, _ := w.ownJIDs()
	if !req.TargetFromMe {
		var err error
		switch {
		case to.Server == types.GroupServer:
			if author, err = parseOptJID(req.TargetSender); err != nil || author.IsEmpty() {
				return nil, invalid("the poll's author is required in groups")
			}
		default:
			author = to
		}
	}
	secret, stored, err := w.pollSecret(ctx, to, author, req.TargetId)
	if err != nil {
		return nil, err
	}
	plaintext, err := proto.Marshal(&waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions(req.GetPoll().GetOptions())})
	if err != nil {
		return nil, err
	}
	key, ad := msgSecretKey(secretPollVote, w.ownFor(stored), req.TargetId, stored, secret)
	ct, iv, err := sealSecret(key, ad, plaintext)
	if err != nil {
		return nil, err
	}
	return &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
		PollCreationMessageKey: eventKey(to, req.TargetId, req.TargetFromMe, stored),
		Vote:                   &waE2E.PollEncValue{EncPayload: ct, EncIV: iv},
		SenderTimestampMS:      proto.Int64(time.Now().UnixMilli()),
	}}, nil
}
