package worker

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// CHT-9: the timer change of a direct chat is the EPHEMERAL_SETTING protocol message.
func TestBuildDisappearing(t *testing.T) {
	w, _ := composeAdapter(t)
	ctx := context.Background()
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "disappearing", DisappearingSeconds: 604800})
	pm := m.GetProtocolMessage()
	if err != nil || pm.GetType() != waE2E.ProtocolMessage_EPHEMERAL_SETTING || pm.GetEphemeralExpiration() != 604800 ||
		pm.GetEphemeralSettingTimestamp() == 0 {
		t.Fatalf("disappearing = %v %v", m, err)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "disappearing"})
	if m.GetProtocolMessage().EphemeralExpiration == nil || m.GetProtocolMessage().GetEphemeralExpiration() != 0 {
		t.Fatalf("off must send expiration 0: %v", m)
	}
	group := types.NewJID("120363000000000001", types.GroupServer)
	for _, req := range []*workerv1.SendRequest{
		{Type: "disappearing", DisappearingSeconds: 3600},
	} {
		if _, err := w.build(ctx, dm, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", req, err)
		}
	}
	if _, err := w.build(ctx, group, &workerv1.SendRequest{Type: "disappearing", DisappearingSeconds: 86400}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("group: %v", err)
	}
}

// Messages sent in a chat with a timer carry it in their ContextInfo (whatsmeow does
// not add it), keeping their reply/mention context.
func TestApplyEphemeral(t *testing.T) {
	w, dir := composeAdapter(t)
	ctx := context.Background()
	m, _ := w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "oi"})
	applyEphemeral(m, 86400, 1_800_000_000)
	if m.Conversation != nil || m.GetExtendedTextMessage().GetText() != "oi" ||
		m.GetExtendedTextMessage().GetContextInfo().GetExpiration() != 86400 ||
		m.GetExtendedTextMessage().GetContextInfo().GetEphemeralSettingTimestamp() != 1_800_000_000 {
		t.Fatalf("text = %v", m)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "text", Text: "x", Quote: &workerv1.Quote{MessageId: "Q1", Text: "q"}})
	applyEphemeral(m, 604800, 0)
	ci := m.GetExtendedTextMessage().GetContextInfo()
	if ci.GetStanzaID() != "Q1" || ci.GetExpiration() != 604800 || ci.EphemeralSettingTimestamp != nil {
		t.Fatalf("reply = %v", ci)
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "image", Mime: "image/png", MediaPath: stage(t, dir, "i", []byte("png"))})
	applyEphemeral(m, 7776000, 0)
	if m.GetImageMessage().GetContextInfo().GetExpiration() != 7776000 {
		t.Fatalf("image = %v", m.GetImageMessage().GetContextInfo())
	}
	m, _ = w.build(ctx, dm, &workerv1.SendRequest{Type: "poll", Poll: &workerv1.Poll{Name: "Q?", Options: []string{"a", "b"}}})
	applyEphemeral(m, 86400, 0)
	if m.GetPollCreationMessage().GetContextInfo().GetExpiration() != 86400 {
		t.Fatal("poll without expiration")
	}
	r := &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("+1")}}
	applyEphemeral(r, 86400, 0) // no context: left alone
	if r.ReactionMessage.Text == nil || r.ExtendedTextMessage != nil {
		t.Fatalf("reaction = %v", r)
	}
}

// A timer change received from the contact (or another device) becomes a ChatUpdate, and a
// received message reports the timer it carries.
func TestDisappearingReceived(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	ts := time.Unix(1_800_000_000, 0)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: dm, Sender: dm}, ID: "E1", Timestamp: ts}
	w.handle(&events.Message{Info: info, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(), EphemeralExpiration: proto.Uint32(86400),
		EphemeralSettingTimestamp: proto.Int64(1_799_999_000)}}})
	info.ID = "M1"
	w.handle(&events.Message{Info: info, Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("some"), ContextInfo: &waE2E.ContextInfo{Expiration: proto.Uint32(86400), EphemeralSettingTimestamp: proto.Int64(1_799_999_000)}}}})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 2 {
		t.Fatalf("events = %v", r.evs)
	}
	u := r.evs[0].GetChatUpdate()
	if u.GetField() != "disappearing" || u.GetChat() != dm.String() || u.GetDisappearingSeconds() != 86400 ||
		u.GetTimestampMs() != 1_799_999_000_000 || u.GetFromMe() {
		t.Fatalf("chat update = %v", u)
	}
	if m := r.evs[1].GetMessage(); m.GetEphemeralSeconds() != 86400 || m.GetEphemeralSettingTs() != 1_799_999_000 {
		t.Fatalf("message = %v", m)
	}
}
