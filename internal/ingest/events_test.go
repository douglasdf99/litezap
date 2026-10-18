package ingest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
)

type recPub struct {
	mu  sync.Mutex
	evs []pubEv
}

type pubEv struct {
	instance, typ string
	data          any
}

func (r *recPub) Publish(id, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, pubEv{id, typ, data})
}

func (r *recPub) snapshot() []pubEv {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pubEv(nil), r.evs...)
}

func receipt(typ string, ids ...string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: &workerv1.Receipt{MessageIds: ids, Chat: chat, Type: typ}}}
}

func TestEventsForNewMessagesOnly(t *testing.T) {
	in, _ := setup(t)
	pub := &recPub{}
	in.Events = pub
	recv := &workerv1.Message{Id: "WA1", Chat: chat, Sender: chat, PushName: "João", TimestampMs: ts, Type: "image",
		Text: "foto", Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 9, Ref: []byte("r")}}
	in.WorkerEvent("i1", msgEv(recv))
	in.WorkerEvent("i1", msgEv(recv)) // duplicate: no second event
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "R1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "reaction", Text: "+1", ReactionTo: "X"}))
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "P1", Chat: chat, FromMe: true, TimestampMs: ts, Type: "text", Text: "eu"}))
	evs := pub.snapshot()
	var types []string
	for _, e := range evs {
		types = append(types, e.typ)
	}
	want := []string{events.TypeMessageReceived, events.TypeMessageReaction, events.TypeMessageSent}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	m := evs[0].data.(events.Message)
	if m.ID == "" || m.WAMessageID != "WA1" || m.Source != "contact" || m.PushName != "João" || m.Media == nil ||
		m.Media.ID == "" || m.Media.URL != "/v1/instances/i1/media/"+m.Media.ID {
		t.Fatalf("message data = %+v", m)
	}
	if s := evs[2].data.(events.Message); s.Source != "device" || !s.FromMe {
		t.Fatalf("sent data = %+v", s)
	}

	// Receipts: only rows whose status rose produce message.status.
	in.WorkerEvent("i1", receipt("delivered", "P1", "UNKNOWN"))
	in.WorkerEvent("i1", receipt("delivered", "P1")) // no change
	in.WorkerEvent("i1", receipt("played", "P1"))
	sts := pub.snapshot()[3:]
	if len(sts) != 2 {
		t.Fatalf("status events = %+v", sts)
	}
	d := sts[0].data.(events.MessageStatus)
	r := sts[1].data.(events.MessageStatus)
	if d.Status != "delivered" || d.WAMessageID != "P1" || d.Chat != chat || d.ID == "" || r.Status != "read" {
		t.Fatalf("status data = %+v, %+v", d, r)
	}
}

func TestHistoryFeedsTrackerWithoutMessageEvents(t *testing.T) {
	in, _ := setup(t)
	pub := &recPub{}
	in.Events = pub
	in.History = events.NewHistoryTracker(pub, clock.NewFake(time.Unix(0, 0)), time.Second)
	chunk := &workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{
		Chat: chat, Messages: []*workerv1.Message{
			{Id: "H1", Chat: chat, Sender: chat, TimestampMs: ts, Type: "text", Text: "a"},
			{Id: "H2", Chat: chat, Sender: chat, TimestampMs: ts + 1, Type: "text", Text: "b"},
		}}}}
	in.WorkerEvent("i1", chunk)
	in.WorkerEvent("i1", chunk) // same messages again: counted, but not new
	in.History.Close()
	evs := pub.snapshot()
	if len(evs) != 1 || evs[0].typ != events.TypeHistorySynced {
		t.Fatalf("events = %+v", evs)
	}
	if h := evs[0].data.(events.HistorySynced); h != (events.HistorySynced{Chats: 2, Messages: 4, NewMessages: 2}) {
		t.Fatalf("history = %+v", h)
	}
}
