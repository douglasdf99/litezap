package worker

import (
	"bytes"
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// memSecrets keeps message secrets by message id (chat and sender are not checked).
type memSecrets struct {
	secret map[types.MessageID][]byte
	sender map[types.MessageID]types.JID
}

func (m *memSecrets) PutMessageSecrets(ctx context.Context, in []store.MessageSecretInsert) error {
	for _, x := range in {
		_ = m.PutMessageSecret(ctx, x.Chat, x.Sender, x.ID, x.Secret)
	}
	return nil
}
func (m *memSecrets) PutMessageSecret(_ context.Context, _, sender types.JID, id types.MessageID, secret []byte) error {
	m.secret[id], m.sender[id] = secret, sender
	return nil
}
func (m *memSecrets) GetMessageSecret(_ context.Context, _, _ types.JID, id types.MessageID) ([]byte, types.JID, error) {
	return m.secret[id], m.sender[id], nil
}

func eventAdapter(t *testing.T) (*Whatsmeow, *memSecrets) {
	t.Helper()
	w := newMsgAdapter(t)
	sec := &memSecrets{secret: map[types.MessageID][]byte{}, sender: map[types.MessageID]types.JID{}}
	w.cli.Store.MsgSecrets = sec
	return w, sec
}

var testEvent = &workerv1.EventInfo{Name: "Churrasco", Description: "Traga gelo", StartTime: 1_900_000_000, EndTime: 1_900_007_200,
	Location: &workerv1.Location{Name: "Clube", Address: "Rua Exemplo, 100"}, ExtraGuestsAllowed: true}

// MSG-24: a new event is an EventMessage with its own message secret.
func TestBuildEvent(t *testing.T) {
	w, _ := eventAdapter(t)
	ctx := context.Background()
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "event", Event: testEvent, Quote: &workerv1.Quote{MessageId: "Q1", Text: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	em := m.GetEventMessage()
	if em.GetName() != "Churrasco" || em.GetStartTime() != 1_900_000_000 || em.GetEndTime() != 1_900_007_200 ||
		em.GetLocation().GetAddress() != "Rua Exemplo, 100" || em.GetLocation().DegreesLatitude != nil || !em.GetExtraGuestsAllowed() ||
		em.GetIsCanceled() || len(m.GetMessageContextInfo().GetMessageSecret()) != 32 || em.GetContextInfo().GetStanzaID() != "Q1" {
		t.Fatalf("event = %v", m)
	}
	for _, bad := range []*workerv1.EventInfo{nil, {Name: "x"}, {StartTime: 1}, {Name: "x", StartTime: 10, EndTime: 5}} {
		if _, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "event", Event: bad}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// Received: an event message is logged with its details.
	if typ, text, _, _, ok := content(m); typ != "event" || text != "Churrasco" || !ok {
		t.Fatalf("content = %s %s", typ, text)
	}
	out := &workerv1.Message{}
	details(m, out)
	if out.Event.GetName() != "Churrasco" || out.Event.GetLocation().GetName() != "Clube" || out.QuotedId != "Q1" {
		t.Fatalf("details = %v", out.Event)
	}
}

// An edit of our event is a SecretEncryptedMessage EVENT_EDIT that whatsmeow's own
// decryption opens (what the contact's phone does).
func TestEventEditDecryptsWithWhatsmeow(t *testing.T) {
	w, sec := eventAdapter(t)
	ctx := context.Background()
	own, _ := w.ownJIDs()
	_ = sec.PutMessageSecret(ctx, dm, own, "EV1", bytes.Repeat([]byte{3}, 32))
	edited := proto.Clone(testEvent).(*workerv1.EventInfo)
	edited.Name, edited.Canceled = "Churrasco (cancelado)", true
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "edit", TargetId: "EV1", TargetFromMe: true, Event: edited})
	if err != nil {
		t.Fatal(err)
	}
	sm := m.GetSecretEncryptedMessage()
	if sm.GetSecretEncType() != waE2E.SecretEncryptedMessage_EVENT_EDIT || sm.GetTargetMessageKey().GetID() != "EV1" || !sm.GetTargetMessageKey().GetFromMe() {
		t.Fatalf("edit = %v", m)
	}
	evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: dm, Sender: own, IsFromMe: true}, ID: "ED1",
		Timestamp: time.Now()}, Message: m}
	dec, err := w.cli.DecryptSecretEncryptedMessage(ctx, evt)
	if err != nil {
		t.Fatalf("whatsmeow cannot decrypt our event edit: %v", err)
	}
	if dec.GetEventMessage().GetName() != "Churrasco (cancelado)" || !dec.GetEventMessage().GetIsCanceled() {
		t.Fatalf("decrypted = %v", dec)
	}
	// The same edit received from another device becomes an edit with the event.
	r := w.sink.(*dataRec)
	w.handle(evt)
	r.mu.Lock()
	u := r.evs[len(r.evs)-1].GetMessage()
	r.mu.Unlock()
	if u.GetType() != "edit" || u.GetTargetId() != "EV1" || u.GetText() != "Churrasco (cancelado)" || !u.GetEvent().GetCanceled() {
		t.Fatalf("update = %v", u)
	}
	if _, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "edit", TargetId: "EV9", TargetFromMe: true, Event: edited}); err == nil {
		t.Fatal("edit without the event secret accepted")
	} else if c, code := codeOf(err); c != codes.FailedPrecondition || code != "event_secret_missing" {
		t.Fatalf("missing secret = %v", err)
	}
}

// A response to a contact's event is encrypted like a poll vote and opens on the other
// side (the event author receiving it).
func TestEventResponseRoundTrip(t *testing.T) {
	w, sec := eventAdapter(t)
	ctx := context.Background()
	own, _ := w.ownJIDs()
	_ = sec.PutMessageSecret(ctx, dm, dm, "EV2", bytes.Repeat([]byte{5}, 32)) // the contact's event
	m, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "event_response", TargetId: "EV2", EventResponse: "going", ExtraGuests: 2})
	if err != nil {
		t.Fatal(err)
	}
	enc := m.GetEncEventResponseMessage()
	if enc.GetEventCreationMessageKey().GetID() != "EV2" || enc.GetEventCreationMessageKey().GetFromMe() || len(enc.GetEncIV()) != 12 {
		t.Fatalf("response = %v", m)
	}
	// On the author's side the response comes from us, in the chat with us.
	in := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own}, ID: "R1", Timestamp: time.Now()}, Message: m}
	in.Message.EncEventResponseMessage.EventCreationMessageKey.RemoteJID = proto.String(dm.String())
	r, err := w.decryptEventResponse(ctx, in)
	if err != nil {
		t.Fatalf("decrypt = %v", err)
	}
	if r.GetResponse() != waE2E.EventResponseMessage_GOING || r.GetExtraGuestCount() != 2 {
		t.Fatalf("decrypted = %v", r)
	}
	for _, bad := range []*workerv1.SendRequest{
		{Type: "event_response", TargetId: "EV2", EventResponse: "yes"},
		{Type: "event_response", TargetId: "EV2", EventResponse: "going", ExtraGuests: 50},
		{Type: "event_response", EventResponse: "going"},
	} {
		if c, _ := codeOf(func() error { _, err := w.build(ctx, dm, bad); return err }()); c != codes.InvalidArgument {
			t.Errorf("%v: %v", bad, c)
		}
	}
}

// Received responses become event_response updates.
func TestEventResponseReceived(t *testing.T) {
	w, _ := eventAdapter(t)
	w.decryptEventResp = func(context.Context, *events.Message) (*waE2E.EventResponseMessage, error) {
		return &waE2E.EventResponseMessage{Response: waE2E.EventResponseMessage_MAYBE.Enum()}, nil
	}
	r := w.sink.(*dataRec)
	w.handle(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: dm, Sender: dm}, ID: "R2", Timestamp: time.Now()},
		Message: &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
			EventCreationMessageKey: eventKey(dm, "EV3", true, types.EmptyJID)}}})
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.evs[len(r.evs)-1].GetMessage()
	if u.GetType() != "event_response" || u.GetTargetId() != "EV3" || u.GetEventResponse() != "maybe" || u.GetSender() != dm.String() {
		t.Fatalf("update = %v", u)
	}
}
