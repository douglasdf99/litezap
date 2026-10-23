package worker

import (
	"bytes"
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// MSG-23: a vote on a contact's poll is encrypted like whatsmeow's BuildPollVote and opens
// on the poll author's side.
func TestPollVoteRoundTrip(t *testing.T) {
	w, sec := eventAdapter(t)
	ctx := context.Background()
	own, _ := w.ownJIDs()
	_ = sec.PutMessageSecret(ctx, dm, dm, "PL1", bytes.Repeat([]byte{7}, 32)) // the contact's poll
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "poll_vote", TargetId: "PL1",
		Poll: &workerv1.Poll{Options: []string{"sim", "talvez"}}})
	if err != nil {
		t.Fatal(err)
	}
	pu := m.GetPollUpdateMessage()
	if pu.GetPollCreationMessageKey().GetID() != "PL1" || pu.GetPollCreationMessageKey().GetFromMe() ||
		len(pu.GetVote().GetEncIV()) != 12 || pu.GetSenderTimestampMS() == 0 {
		t.Fatalf("vote = %v", m)
	}
	// On the author's side the vote comes from us, in the chat with us.
	in := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own}, ID: "PV1", Timestamp: time.Now()}, Message: m}
	in.Message.PollUpdateMessage.PollCreationMessageKey.RemoteJID = proto.String(dm.String())
	v, err := w.decryptPollVote(ctx, in)
	if err != nil {
		t.Fatalf("decrypt = %v", err)
	}
	want := whatsmeow.HashPollOptions([]string{"sim", "talvez"})
	if len(v.GetSelectedOptions()) != 2 || !bytes.Equal(v.GetSelectedOptions()[0], want[0]) || !bytes.Equal(v.GetSelectedOptions()[1], want[1]) {
		t.Fatalf("decrypted = %v", v)
	}
	// No options: the vote is removed (an empty selection).
	m, err = w.build(ctx, dm, &workerv1.SendRequest{Type: "poll_vote", TargetId: "PL1"})
	if err != nil {
		t.Fatal(err)
	}
	in.Message = m
	in.Message.PollUpdateMessage.PollCreationMessageKey.RemoteJID = proto.String(dm.String())
	if v, err := w.decryptPollVote(ctx, in); err != nil || len(v.GetSelectedOptions()) != 0 {
		t.Fatalf("removal = %v, %v", v, err)
	}
	// Errors: no poll, author unknown in a group, unknown secret.
	group := types.NewJID("120363000000000002", types.GroupServer)
	for to, req := range map[types.JID]*workerv1.SendRequest{
		dm:    {Type: "poll_vote"},
		group: {Type: "poll_vote", TargetId: "PL1"},
	} {
		if c, _ := codeOf(func() error { _, err := w.build(ctx, to, req); return err }()); c != codes.InvalidArgument {
			t.Errorf("%v: %v", req, c)
		}
	}
	if c, reason := codeOf(func() error {
		_, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "poll_vote", TargetId: "OLD"})
		return err
	}()); c != codes.FailedPrecondition || reason != "poll_secret_missing" {
		t.Fatalf("unknown poll: %v %q", c, reason)
	}
}

// Our own vote from another device (by LID in a group) is reported as from_me, so the
// supervisor counts it once, as "me".
func TestOwnPollVoteFromOtherDevice(t *testing.T) {
	w, _ := eventAdapter(t)
	w.decryptPoll = func(context.Context, *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"sim"})}, nil
	}
	ownLID := types.NewJID("999", types.HiddenUserServer)
	w.cli.Store.LID = ownLID
	group := types.NewJID("120363000000000001", types.GroupServer)
	w.handle(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: ownLID, IsGroup: true},
		ID: "PV2", Timestamp: time.Now()}, Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
		PollCreationMessageKey: eventKey(group, "PL2", true, ownLID)}}})
	r := w.sink.(*dataRec)
	r.mu.Lock()
	defer r.mu.Unlock()
	if u := r.evs[len(r.evs)-1].GetMessage(); u.GetType() != "poll_vote" || u.GetTargetId() != "PL2" || !u.GetFromMe() {
		t.Fatalf("own vote = %v", u)
	}
}
