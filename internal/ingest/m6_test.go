package ingest

import (
	"context"
	"crypto/sha256"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
)

func TestStructuredMessagesAndUpdates(t *testing.T) {
	in, st := setup(t)
	pub := &recPub{}
	in.Events = pub
	ctx := context.Background()
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "POLL1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "poll", Text: "Vem?",
		QuotedId: "Q0", Poll: &workerv1.Poll{Name: "Vem?", Options: []string{"sim", "não"}, Selectable: 1}}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "L1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "location",
		Location: &workerv1.Location{Latitude: 1, Longitude: 2, Name: "Casa"}}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "T1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "text", Text: "antes"}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "E1", Chat: chat, Sender: chat, TimestampMs: ts + 1000, Type: "edit", Text: "depois", TargetId: "T1"}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "R1", Chat: chat, Sender: chat, TimestampMs: ts + 2000, Type: "revoke", TargetId: "L1"}))
	sim := sha256.Sum256([]byte("sim"))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "V1", Chat: chat, Sender: chat, PushName: "Ana", TimestampMs: ts + 3000,
		Type: "poll_vote", TargetId: "POLL1", PollHashes: [][]byte{sim[:], []byte("bogus")}}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "V2", Chat: chat, Sender: chat, TimestampMs: ts + 4000, Type: "poll_vote", TargetId: "NOPE"}))

	list, _, err := st.ListMessages(ctx, "i1", chat, 10, nil)
	if err != nil || len(list) != 3 {
		t.Fatalf("log = %+v, %v (updates must not be logged)", list, err)
	}
	byWA := map[string]store.Message{}
	for _, m := range list {
		byWA[m.WAMessageID] = m
	}
	if p := store.ParseMeta(byWA["POLL1"].MetaJSON); p.Poll == nil || p.ReplyTo != "Q0" {
		t.Fatalf("poll meta = %q", byWA["POLL1"].MetaJSON)
	}
	if m := byWA["T1"]; m.Text != "depois" || m.EditedAt.IsZero() {
		t.Fatalf("edited = %+v", m)
	}
	if m := byWA["L1"]; m.RevokedAt.IsZero() {
		t.Fatalf("revoked = %+v", m)
	}
	evs := pub.snapshot()
	var types []string
	for _, e := range evs {
		types = append(types, e.typ)
	}
	want := []string{events.TypeMessageReceived, events.TypeMessageReceived, events.TypeMessageReceived,
		events.TypeMessageEdited, events.TypeMessageRevoked, events.TypeMessagePollVote, events.TypeMessagePollVote}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v", types)
		}
	}
	if m := evs[1].data.(events.Message); m.Location == nil || m.Location.Name != "Casa" {
		t.Fatalf("location event = %+v", m)
	}
	if u := evs[3].data.(events.MessageUpdate); u.ID != byWA["T1"].ID || u.Text != "depois" || u.Source != "contact" {
		t.Fatalf("edited event = %+v", u)
	}
	v := evs[5].data.(events.PollVote)
	if v.PollID != byWA["POLL1"].ID || len(v.SelectedOptions) != 1 || v.SelectedOptions[0] != "sim" || v.UnknownOptions != 1 || v.PushName != "Ana" {
		t.Fatalf("vote = %+v", v)
	}
	if v := evs[6].data.(events.PollVote); v.PollID != "" || len(v.SelectedOptions) != 0 || v.SelectedOptions == nil {
		t.Fatalf("vote on unknown poll = %+v", v)
	}
}
