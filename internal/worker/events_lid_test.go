package worker

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Real-device bug (2026-10-23): an event we sent to a LID-addressed group got a "going"
// from a participant, and the response never became an event_response. These tests use
// whatsmeow's real SQL store (secret lookups with the PN <-> LID mapping) instead of the
// in-memory fake, and responses encrypted the way the other phone does.

var (
	selfPN  = types.NewJID("5511999999999", types.DefaultUserServer)
	selfLID = types.NewJID("100000000000001", types.HiddenUserServer)
	peerPN  = types.NewJID("5511888887777", types.DefaultUserServer)
	peerLID = types.NewJID("200000000000002", types.HiddenUserServer)
	lidGrp  = types.NewJID("120363000000000009", types.GroupServer)
)

// sqlDevice is a logged-in device on a real whatsmeow SQL store, which knows the
// phone-number <-> LID mappings of maps.
func sqlDevice(t *testing.T, pn, lid types.JID, maps ...[2]types.JID) *store.Device {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "s.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := sqlstore.NewWithDB(db, "sqlite3", nil)
	if err := c.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	d := c.NewDevice()
	adPN, adLID := pn, lid
	adPN.Device, adLID.Device = 3, 3
	d.ID, d.LID = &adPN, adLID
	d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{0}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := d.Save(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range maps { // {lid, pn}
		if err := d.LIDs.PutLIDMapping(ctx, m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func sqlAdapter(t *testing.T, logs io.Writer) *Whatsmeow {
	t.Helper()
	d := sqlDevice(t, selfPN, selfLID, [2]types.JID{selfLID, selfPN}, [2]types.JID{peerLID, peerPN})
	if logs == nil {
		logs = io.Discard
	}
	w := NewWhatsmeow(whatsmeow.NewClient(d, nil), slog.New(slog.NewTextHandler(logs, nil)))
	w.sink = &dataRec{}
	return w
}

// sealAs encrypts plaintext the way the responder's phone does, deriving the key with
// the given identities of the original sender and of the responder.
func sealAs(t *testing.T, useCase string, responder types.JID, id string, origSender types.JID, secret, plaintext []byte) (ct, iv []byte) {
	t.Helper()
	k, ad := msgSecretKey(useCase, responder, id, origSender, secret)
	ct, iv, err := sealSecret(k, ad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return ct, iv
}

func lastUpdate(t *testing.T, w *Whatsmeow) *workerv1.Message {
	t.Helper()
	r := w.sink.(*dataRec)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evs) == 0 {
		return nil
	}
	return r.evs[len(r.evs)-1].GetMessage()
}

// A participant answers our event in a LID-addressed group. whatsmeow's SendMessage
// stored the event's secret under our LID (the group's addressing). The response
// arrives from the participant's LID, with the participant's phone number as SenderAlt,
// and the event key names us by LID or by phone number. Whatever identities the phone
// fed into the key derivation, the response must become an event_response.
func TestEventResponseInLIDGroup(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	plain, _ := proto.Marshal(&waE2E.EventResponseMessage{Response: waE2E.EventResponseMessage_GOING.Enum(), TimestampMS: proto.Int64(1)})
	cases := []struct {
		name            string
		keyParticipant  types.JID // how the response's key names the event's author (us)
		origKDF, modKDF types.JID // identities the phone derived the key with
	}{
		{"all LID", selfLID, selfLID, peerLID},
		{"all phone numbers", selfLID, selfPN, peerPN}, // the device case
		{"key by PN, KDF by PN", selfPN, selfPN, peerPN},
		{"author LID, responder PN", selfLID, selfLID, peerPN},
		{"author PN, responder LID", selfLID, selfPN, peerLID},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			w := sqlAdapter(t, nil)
			id := "3EB0EVENT" + string(rune('A'+i))
			// What SendMessage does for a LID-addressed group (send.go): (chat, own LID, id).
			if err := w.cli.Store.MsgSecrets.PutMessageSecret(ctx, lidGrp, selfLID, types.MessageID(id), secret); err != nil {
				t.Fatal(err)
			}
			ct, iv := sealAs(t, secretEventResponse, c.modKDF, id, c.origKDF, secret, plain)
			key := eventKey(lidGrp, id, false, c.keyParticipant)
			evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: lidGrp, Sender: peerLID,
				SenderAlt: peerPN, IsGroup: true, AddressingMode: types.AddressingModeLID}, ID: "RESP1", Timestamp: time.Now()},
				Message: &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
					EventCreationMessageKey: key, EncPayload: ct, EncIV: iv}}}
			w.handle(evt)
			u := lastUpdate(t, w)
			if u.GetType() != "event_response" || u.GetTargetId() != id || u.GetEventResponse() != "going" ||
				u.GetSender() != peerPN.String() || u.GetFromMe() || u.GetChat() != lidGrp.String() {
				t.Fatalf("update = %v", u)
			}
		})
	}
}

// A response that cannot be opened is logged with the reason, without its content or
// any phone number.
func TestEventResponseFailureLogged(t *testing.T) {
	var logs bytes.Buffer
	w := sqlAdapter(t, &logs)
	ctx := context.Background()
	_ = w.cli.Store.MsgSecrets.PutMessageSecret(ctx, lidGrp, selfLID, "EVX", bytes.Repeat([]byte{1}, 32))
	mk := func(id string) *events.Message {
		return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: lidGrp, Sender: peerLID, SenderAlt: peerPN,
			IsGroup: true}, ID: "R" + types.MessageID(id), Timestamp: time.Now()},
			Message: &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
				EventCreationMessageKey: eventKey(lidGrp, id, false, selfLID), EncPayload: []byte("garbage-garbage-garbage"), EncIV: make([]byte, 12)}}}
	}
	w.handle(mk("EVX")) // secret known, wrong key
	w.handle(mk("EVY")) // secret unknown
	out := logs.String()
	if !strings.Contains(out, "reason=no_key_matched") || !strings.Contains(out, "reason=secret_missing") {
		t.Fatalf("logs = %s", out)
	}
	for _, leak := range []string{selfPN.User, selfLID.User, peerPN.User, peerLID.User} {
		if strings.Contains(out, leak) {
			t.Fatalf("logs leak %s: %s", leak, out)
		}
	}
	if u := lastUpdate(t, w); u != nil {
		t.Fatalf("undecryptable response reported: %v", u)
	}
}

// MSG-23: a vote on our poll in a LID-addressed group, encrypted by whatsmeow's own
// EncryptPollVote on the voter's side. The voter's client knows our poll by phone
// number, so it derives the key with phone numbers (its own and ours), while the vote
// reaches us from its LID. whatsmeow's DecryptPollVote only tries the vote's sender as
// is, and drops the vote.
func TestPollVoteInLIDGroup(t *testing.T) {
	ctx := context.Background()
	w := sqlAdapter(t, nil)
	secret := bytes.Repeat([]byte{9}, 32)
	const pollID = "3EB0POLL1"
	if err := w.cli.Store.MsgSecrets.PutMessageSecret(ctx, lidGrp, selfLID, pollID, secret); err != nil {
		t.Fatal(err)
	}
	for _, author := range []types.JID{selfPN, selfLID} {
		t.Run(author.Server, func(t *testing.T) {
			voter := whatsmeow.NewClient(sqlDevice(t, peerPN, peerLID), nil)
			if err := voter.Store.MsgSecrets.PutMessageSecret(ctx, lidGrp, author, pollID, secret); err != nil {
				t.Fatal(err)
			}
			pollInfo := &types.MessageInfo{MessageSource: types.MessageSource{Chat: lidGrp, Sender: author, IsGroup: true}, ID: pollID}
			vote, err := voter.BuildPollVote(ctx, pollInfo, []string{"Sim"})
			if err != nil {
				t.Fatal(err)
			}
			w.handle(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: lidGrp, Sender: peerLID,
				SenderAlt: peerPN, IsGroup: true, AddressingMode: types.AddressingModeLID}, ID: "VOTE" + types.MessageID(author.Server),
				Timestamp: time.Now()}, Message: vote})
			u := lastUpdate(t, w)
			if u.GetType() != "poll_vote" || u.GetTargetId() != pollID || len(u.GetPollHashes()) != 1 ||
				!bytes.Equal(u.GetPollHashes()[0], whatsmeow.HashPollOptions([]string{"Sim"})[0]) {
				t.Fatalf("update = %v", u)
			}
		})
	}
}
