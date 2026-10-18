package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func lastEvent(t *testing.T, w *Whatsmeow) *workerv1.Event {
	t.Helper()
	r := w.sink.(*dataRec)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) == 0 {
		t.Fatal("no event")
	}
	return r.evs[len(r.evs)-1]
}

func eventCount(w *Whatsmeow) int {
	r := w.sink.(*dataRec)
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.evs)
}

func TestChatPresenceStates(t *testing.T) {
	w := newMsgAdapter(t)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	lid := types.NewJID("123456", types.HiddenUserServer)
	src := types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn}
	cases := []struct {
		state types.ChatPresence
		media types.ChatPresenceMedia
		want  string
	}{
		{types.ChatPresenceComposing, "", "composing"},
		{types.ChatPresenceComposing, types.ChatPresenceMediaAudio, "recording"},
		{types.ChatPresencePaused, "", "paused"},
	}
	for _, c := range cases {
		w.handle(&events.ChatPresence{MessageSource: src, State: c.state, Media: c.media})
		cp := lastEvent(t, w).GetChatPresence()
		if cp.GetState() != c.want || cp.GetChat() != pn.String() || cp.GetSender() != pn.String() || cp.GetIsGroup() {
			t.Errorf("%s/%s -> %+v", c.state, c.media, cp)
		}
	}
	n := eventCount(w)
	own := src
	own.IsFromMe = true
	w.handle(&events.ChatPresence{MessageSource: own, State: types.ChatPresenceComposing})
	w.handle(&events.ChatPresence{MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: pn}, State: types.ChatPresenceComposing})
	if eventCount(w) != n {
		t.Fatal("own and broadcast chat states must be ignored")
	}
}

func TestPresenceEvent(t *testing.T) {
	w := newMsgAdapter(t)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	seen := time.Unix(1_800_000_000, 0)
	w.handle(&events.Presence{From: pn, Unavailable: true, LastSeen: seen})
	p := lastEvent(t, w).GetPresence()
	if p.GetJid() != pn.String() || p.GetOnline() || p.GetLastSeenMs() != seen.UnixMilli() {
		t.Fatalf("presence = %+v", p)
	}
	w.handle(&events.Presence{From: pn})
	if p := lastEvent(t, w).GetPresence(); !p.GetOnline() || p.GetLastSeenMs() != 0 {
		t.Fatalf("online = %+v", p)
	}
}

func TestCallEventsAndReject(t *testing.T) {
	w := newMsgAdapter(t)
	var rejected []string
	w.rejectCall = func(_ context.Context, from types.JID, id string) error {
		rejected = append(rejected, from.String()+"/"+id)
		return nil
	}
	lid := types.NewJID("123456", types.HiddenUserServer)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	from := types.JID{User: "123456", Server: types.HiddenUserServer, Device: 3}
	meta := types.BasicCallMeta{From: from, CallCreator: lid, CallCreatorAlt: pn, CallID: "C1", Timestamp: time.Unix(1_800_000_000, 0)}
	video := &waBinary.Node{Tag: "offer", Content: []waBinary.Node{{Tag: "audio"}, {Tag: "video"}}}
	w.handle(&events.CallOffer{BasicCallMeta: meta, Data: video})
	c := lastEvent(t, w).GetCall()
	if c.GetKind() != "offer" || c.GetCallId() != "C1" || c.GetCaller() != pn.String() || c.GetMedia() != "video" ||
		c.GetTimestampMs() != meta.Timestamp.UnixMilli() || c.GetGroup() != "" {
		t.Fatalf("offer = %+v", c)
	}
	if err := w.RejectCall(context.Background(), "C1"); err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || rejected[0] != lid.String()+"/C1" { // the creator, without device
		t.Fatalf("rejected = %v", rejected)
	}
	// Rejected calls are forgotten; unknown ids are never rejected.
	for _, id := range []string{"C1", "nope"} {
		if err := w.RejectCall(context.Background(), id); status.Code(err) != codes.NotFound {
			t.Fatalf("reject %s = %v", id, err)
		}
	}
	w.handle(&events.CallOffer{BasicCallMeta: types.BasicCallMeta{From: pn, CallCreator: pn, CallID: "C2"},
		Data: &waBinary.Node{Tag: "offer", Content: []waBinary.Node{{Tag: "audio"}}}})
	if c := lastEvent(t, w).GetCall(); c.GetMedia() != "audio" || c.GetTimestampMs() == 0 {
		t.Fatalf("audio offer = %+v", c)
	}
	w.handle(&events.CallAccept{BasicCallMeta: types.BasicCallMeta{From: pn, CallCreator: pn, CallID: "C2"}})
	if c := lastEvent(t, w).GetCall(); c.GetKind() != "accept" {
		t.Fatalf("accept = %+v", c)
	}
	w.handle(&events.CallTerminate{BasicCallMeta: types.BasicCallMeta{From: pn, CallCreator: pn, CallID: "C2"}, Reason: "timeout"})
	if c := lastEvent(t, w).GetCall(); c.GetKind() != "terminate" || c.GetReason() != "timeout" {
		t.Fatalf("terminate = %+v", c)
	}
	if err := w.RejectCall(context.Background(), "C2"); status.Code(err) != codes.NotFound {
		t.Fatalf("ended call reject = %v", err)
	}
	group := types.NewJID("1203630", types.GroupServer)
	creator := types.NewJID("5511777776666", types.DefaultUserServer)
	w.handle(&events.CallOfferNotice{BasicCallMeta: types.BasicCallMeta{From: pn, CallCreator: creator, CallID: "G1", GroupJID: group}, Media: "audio", Type: "group"})
	if c := lastEvent(t, w).GetCall(); c.GetKind() != "offer_notice" || c.GetGroup() != group.String() || c.GetCaller() != creator.String() {
		t.Fatalf("group = %+v", c)
	}
	// RejectCall names the call creator, not the stanza's sender.
	if err := w.RejectCall(context.Background(), "G1"); err != nil || rejected[len(rejected)-1] != creator.String()+"/G1" {
		t.Fatalf("group reject = %v %v", err, rejected)
	}
	n := eventCount(w)
	w.handle(&events.CallRelayLatency{BasicCallMeta: meta})
	w.handle(&events.CallTransport{BasicCallMeta: meta})
	if eventCount(w) != n {
		t.Fatal("signalling events must not be reported")
	}
}

func TestCallBookBounded(t *testing.T) {
	var b callBook
	now := time.Now()
	for i := 0; i < maxCalls+10; i++ {
		b.add(fmt.Sprint("c", i), types.EmptyJID, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(b.calls) != maxCalls {
		t.Fatalf("calls = %d", len(b.calls))
	}
	if _, ok := b.get("c0", now); ok {
		t.Fatal("the oldest call must be evicted first")
	}
	b.add("x", types.EmptyJID, now)
	if _, ok := b.get("x", now.Add(callTTL+time.Second)); ok {
		t.Fatal("expired call still known")
	}
}

func TestSetPresenceAndSubscribe(t *testing.T) {
	w := newMsgAdapter(t)
	var states []types.Presence
	w.sendPresence = func(_ context.Context, p types.Presence) error {
		states = append(states, p)
		return nil
	}
	var subs []string
	w.subscribePresence = func(_ context.Context, j types.JID) error {
		subs = append(subs, j.String())
		return nil
	}
	if err := w.SetPresence(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPresence(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != types.PresenceAvailable || states[1] != types.PresenceUnavailable {
		t.Fatalf("states = %v", states)
	}
	w.sendPresence = func(context.Context, types.Presence) error { return whatsmeow.ErrNoPushName }
	if err := w.SetPresence(context.Background(), true); status.Code(err) != codes.Unavailable {
		t.Fatalf("no push name = %v", err)
	}
	if err := w.SubscribePresence(context.Background(), "5511888887777:2@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0] != "5511888887777@s.whatsapp.net" {
		t.Fatalf("subs = %v", subs)
	}
	for _, bad := range []string{"", "1203630@g.us", "status@broadcast"} {
		if err := w.SubscribePresence(context.Background(), bad); status.Code(err) != codes.InvalidArgument {
			t.Errorf("subscribe %q = %v", bad, err)
		}
	}
	w.subscribePresence = func(context.Context, types.JID) error { return whatsmeow.ErrNotConnected }
	if err := w.SubscribePresence(context.Background(), "5511888887777@s.whatsapp.net"); status.Code(err) != codes.Unavailable {
		t.Fatalf("not connected = %v", err)
	}
}

func TestButtonReplies(t *testing.T) {
	cases := []struct {
		m    *waE2E.Message
		want *workerv1.ButtonReply
	}{
		{&waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{SelectedButtonID: proto.String("b1"),
			Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Sim"}}},
			&workerv1.ButtonReply{Kind: "button", Id: "b1", Title: "Sim"}},
		{&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("Plano B"), Description: proto.String("mensal"),
			SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("row2")}}},
			&workerv1.ButtonReply{Kind: "list", Id: "row2", Title: "Plano B", Description: "mensal"}},
		{&waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{SelectedID: proto.String("t1"), SelectedDisplayText: proto.String("Ok")}},
			&workerv1.ButtonReply{Kind: "template", Id: "t1", Title: "Ok"}},
	}
	for _, c := range cases {
		typ, text, _, _, ok := content(c.m)
		if !ok || typ != "button_reply" || text != c.want.Title {
			t.Errorf("content = %s %q %v", typ, text, ok)
		}
		out := &workerv1.Message{}
		details(c.m, out)
		if !proto.Equal(out.ButtonReply, c.want) {
			t.Errorf("reply = %+v, want %+v", out.ButtonReply, c.want)
		}
	}
	quoted := &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("ORIG")}}}
	out := &workerv1.Message{}
	details(quoted, out)
	if out.QuotedId != "ORIG" {
		t.Fatalf("quoted = %q", out.QuotedId)
	}
}

func TestPlayedReceiptForwarded(t *testing.T) {
	w := newMsgAdapter(t)
	pn := types.NewJID("5511888887777", types.DefaultUserServer)
	w.handle(&events.Receipt{MessageSource: types.MessageSource{Chat: pn, Sender: pn},
		MessageIDs: []types.MessageID{"OUT1"}, Type: types.ReceiptTypePlayed})
	if r := lastEvent(t, w).GetReceipt(); r.GetType() != "played" {
		t.Fatalf("receipt = %+v", r)
	}
	n := eventCount(w)
	w.handle(&events.Receipt{MessageSource: types.MessageSource{Chat: pn, Sender: pn, IsFromMe: true},
		MessageIDs: []types.MessageID{"IN1"}, Type: types.ReceiptTypePlayedSelf})
	if eventCount(w) != n {
		t.Fatal("played-self must be ignored")
	}
}

// A reject sent by another device of the account (the phone) is flagged from_self and does
// not make the call unknown: it can still be rejected by us.
func TestRejectFromOwnDevice(t *testing.T) {
	w := newMsgAdapter(t)
	w.rejectCall = func(context.Context, types.JID, string) error { return nil }
	caller := types.NewJID("5511888887777", types.DefaultUserServer)
	ownPhone := types.JID{User: "5511999999999", Server: types.DefaultUserServer, Device: 0}
	w.handle(&events.CallOffer{BasicCallMeta: types.BasicCallMeta{From: caller, CallCreator: caller, CallID: "S1"},
		Data: &waBinary.Node{Tag: "offer", Content: []waBinary.Node{{Tag: "audio"}}}})
	if lastEvent(t, w).GetCall().GetFromSelf() {
		t.Fatal("the caller's offer is not from_self")
	}
	w.handle(&events.CallReject{BasicCallMeta: types.BasicCallMeta{From: ownPhone, CallCreator: caller, CallID: "S1"},
		Data: &waBinary.Node{Tag: "reject", Attrs: waBinary.Attrs{"call-id": "S1", "count": "0"}}})
	if c := lastEvent(t, w).GetCall(); c.GetKind() != "reject" || !c.GetFromSelf() || c.GetCaller() != caller.String() {
		t.Fatalf("reject = %+v", c)
	}
	if err := w.RejectCall(context.Background(), "S1"); err != nil {
		t.Fatalf("call forgotten after own-device reject: %v", err)
	}
	// A reject from the other party ends it.
	w.handle(&events.CallOffer{BasicCallMeta: types.BasicCallMeta{From: caller, CallCreator: caller, CallID: "S2"}})
	w.handle(&events.CallReject{BasicCallMeta: types.BasicCallMeta{From: caller, CallCreator: caller, CallID: "S2"}})
	if c := lastEvent(t, w).GetCall(); c.GetFromSelf() {
		t.Fatalf("peer reject = %+v", c)
	}
	if err := w.RejectCall(context.Background(), "S2"); status.Code(err) != codes.NotFound {
		t.Fatalf("peer-rejected call still known: %v", err)
	}
	tag, children, attrs := callData(&waBinary.Node{Tag: "reject", Attrs: waBinary.Attrs{"count": "0", "call-id": "x"},
		Content: []waBinary.Node{{Tag: "audio"}}})
	if tag != "reject" || len(children) != 1 || len(attrs) != 2 || attrs[0] != "call-id" {
		t.Fatalf("callData = %s %v %v", tag, children, attrs)
	}
}
