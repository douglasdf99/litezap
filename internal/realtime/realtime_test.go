package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/settings"
)

type noWorkers struct{}

func (noWorkers) Client(string) (workerv1.WorkerClient, error) { return nil, errors.New("not running") }

type rec struct {
	mu  sync.Mutex
	evs []struct {
		typ  string
		data any
	}
}

func (r *rec) Publish(_, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, struct {
		typ  string
		data any
	}{typ, data})
}

func newHandler(set settings.Settings, c clock.Clock) (*Handler, *rec) {
	r := &rec{}
	return &Handler{Workers: noWorkers{}, Events: r, Clock: c,
		Settings: func(context.Context, string) (settings.Settings, error) { return set, nil }}, r
}

func call(kind, id string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: id, Kind: kind, Caller: "5511@s.whatsapp.net"}}}
}

// With the worker gone the rejection fails: the call is reported as not rejected and
// ends as missed (nobody answered).
func TestAutoRejectWithoutWorker(t *testing.T) {
	set := settings.Defaults()
	set.Calls.AutoReject = true
	h, r := newHandler(set, nil)
	h.WorkerEvent("i1", call("offer", "C1"))
	h.WorkerEvent("i1", call("offer", "C1")) // replayed offer: one event
	h.WorkerEvent("i1", call("terminate", "C1"))
	if len(r.evs) != 2 || r.evs[0].typ != events.TypeCallReceived || r.evs[0].data.(events.Call).Rejected {
		t.Fatalf("events = %+v", r.evs)
	}
	if e := r.evs[1].data.(events.Call); !e.Missed {
		t.Fatalf("ended = %+v", e)
	}
}

// Signals for calls that were never seen ringing (worker restarted) still end once.
func TestUnknownCallEnds(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	h.WorkerEvent("i1", call("reject", "X1"))
	h.WorkerEvent("i1", call("terminate", "X1"))
	if len(r.evs) != 1 {
		t.Fatalf("events = %+v", r.evs)
	}
	if e := r.evs[0].data.(events.Call); e.Reason != "rejected_by_device" || e.Missed || e.Caller == "" {
		t.Fatalf("ended = %+v", e)
	}
}

func TestPresenceDedupeWindow(t *testing.T) {
	c := clock.NewFake(time.Unix(1_800_000_000, 0))
	h, r := newHandler(settings.Defaults(), c)
	typing := &workerv1.Event{Kind: &workerv1.Event_ChatPresence{ChatPresence: &workerv1.ChatPresence{
		Chat: "5511@s.whatsapp.net", Sender: "5511@s.whatsapp.net", State: "composing"}}}
	h.WorkerEvent("i1", typing)
	h.WorkerEvent("i1", typing)
	c.Advance(presenceDedupe)
	h.WorkerEvent("i1", typing)
	h.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Presence{Presence: &workerv1.Presence{Jid: "5511@s.whatsapp.net", Online: true}}})
	if len(r.evs) != 3 || r.evs[2].data.(events.ChatPresence).State != "online" {
		t.Fatalf("events = %+v", r.evs)
	}
}
