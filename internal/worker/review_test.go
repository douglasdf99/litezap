package worker

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// In a LID group the mention JID becomes the LID: the @token in the text follows it.
func TestMentionInLIDGroupRewritesText(t *testing.T) {
	w, _ := composeAdapter(t)
	group := types.NewJID("120363", types.GroupServer)
	lid := types.NewJID("777", types.HiddenUserServer)
	w.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{AddressingMode: types.AddressingModeLID}, nil
	}
	w.lidFor = func(context.Context, types.JID) (types.JID, error) { return lid, nil }
	req := &workerv1.SendRequest{Type: "text", Text: "oi @5511977776666!", Mentions: []string{"5511977776666@s.whatsapp.net"}}
	m, err := w.build(context.Background(), group, req)
	if err != nil {
		t.Fatal(err)
	}
	ext := m.GetExtendedTextMessage()
	if ext.GetText() != "oi @777!" || ext.GetContextInfo().GetMentionedJID()[0] != "777@lid" {
		t.Fatalf("text = %q, mentions = %v", ext.GetText(), ext.GetContextInfo().GetMentionedJID())
	}
	if req.Text != "oi @5511977776666!" {
		t.Fatal("the request was modified")
	}
}

func TestViewOnceCannotBeForwarded(t *testing.T) {
	w, _ := composeAdapter(t)
	img := &waE2E.ImageMessage{ViewOnce: proto.Bool(true), DirectPath: proto.String("/p")}
	_, err := w.build(context.Background(), dm, &workerv1.SendRequest{Type: "forward", ForwardKind: "image", ForwardRef: mediaRef(img)})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("view-once forward = %v", err)
	}
	_, _, media, _, _ := content(&waE2E.Message{ImageMessage: img})
	if !media.GetViewOnce() {
		t.Fatal("view-once not reported")
	}
}

func TestAdminRevokeAndEmptyEdit(t *testing.T) {
	w := newMsgAdapter(t)
	r := w.sink.(*dataRec)
	group := types.NewJID("120363", types.GroupServer)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: dm, IsGroup: true}, ID: "X1",
		Timestamp: time.Unix(1_800_000_000, 0), Edit: types.EditAttributeAdminRevoke}
	w.handle(&events.Message{Info: info, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("T1")}}}})
	w.handle(&events.Message{Info: info, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("T2")}}}})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) != 1 || !r.evs[0].GetMessage().AdminRevoke {
		t.Fatalf("events = %v", r.evs)
	}
}
