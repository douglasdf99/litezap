package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
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

// Signals for calls that were never seen ringing (worker restarted) still end once; a
// peer reject followed by a terminate ends with the terminate's outcome.
func TestUnknownCallEnds(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	defer h.Close()
	h.WorkerEvent("i1", call("reject", "X1"))
	h.WorkerEvent("i1", call("terminate", "X1"))
	if len(r.evs) != 1 {
		t.Fatalf("events = %+v", r.evs)
	}
	if e := r.evs[0].data.(events.Call); e.Reason != "caller_hangup" || !e.Missed || e.Caller == "" {
		t.Fatalf("ended = %+v", e)
	}
}

// A reject from the other party with no terminate after it ends the call after the grace.
func TestPeerRejectWithoutTerminate(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	h.RejectGrace = 30 * time.Millisecond
	defer h.Close()
	h.WorkerEvent("i1", call("offer", "R1"))
	h.WorkerEvent("i1", call("reject", "R1"))
	if len(r.types()) != 1 {
		t.Fatalf("reject ended the call at once: %v", r.types())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(r.types()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("peer reject never ended the call")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e := r.last(); e.Reason != "rejected_by_peer" || e.Missed {
		t.Fatalf("ended = %+v", e)
	}
}

func TestPresenceDedupeWindow(t *testing.T) {
	c := clock.NewFake(time.Unix(1_800_000_000, 0))
	set := settings.Defaults()
	set.Presence.Online = true
	h, r := newHandler(set, c)
	h.StateChanged("i1", instance.Connected, "") // applies presence.online (worker missing: flag only)
	h.Wait()
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

// Presence that still arrives while presence.online is off (or before it was applied)
// is dropped.
func TestPresenceDroppedWhileOffline(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	h.StateChanged("i1", instance.Connected, "")
	h.Wait()
	h.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_ChatPresence{ChatPresence: &workerv1.ChatPresence{
		Chat: "5511@s.whatsapp.net", State: "composing"}}})
	h.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Presence{Presence: &workerv1.Presence{Jid: "5511@s.whatsapp.net", Online: true}}})
	if len(r.evs) != 0 {
		t.Fatalf("events = %+v", r.evs)
	}
}

// Signals out of order: a call that already ended is reported, never rejected.
func TestOfferAfterEndIsNotRejected(t *testing.T) {
	set := settings.Defaults()
	set.Calls.AutoReject = true
	h, r := newHandler(set, nil)
	h.WorkerEvent("i1", call("terminate", "L1"))
	h.WorkerEvent("i1", call("offer", "L1"))
	if len(r.evs) != 2 || r.evs[1].typ != events.TypeCallReceived || r.evs[1].data.(events.Call).Rejected {
		t.Fatalf("events = %+v", r.evs)
	}
}

func TestCallMemoryEvictsOldest(t *testing.T) {
	c := clock.NewFake(time.Unix(1_800_000_000, 0))
	h, _ := newHandler(settings.Defaults(), c)
	for i := 0; i < maxCalls+5; i++ {
		h.WorkerEvent("i1", call("offer", fmt.Sprint("C", i)))
		c.Advance(time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) > maxCalls {
		t.Fatalf("calls = %d", len(h.calls))
	}
	if _, ok := h.calls["i1|C0"]; ok {
		t.Fatal("oldest call kept")
	}
	if _, ok := h.calls[fmt.Sprint("i1|C", maxCalls+4)]; !ok {
		t.Fatal("newest call evicted")
	}
}

func selfReject(id string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: id, Kind: "reject",
		Caller: "5511@s.whatsapp.net", FromSelf: true}}}
}

func terminate(id, reason string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: id, Kind: "terminate",
		Caller: "5511@s.whatsapp.net", Reason: reason}}}
}

func (r *rec) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		out = append(out, e.typ)
	}
	return out
}

func (r *rec) last() events.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evs[len(r.evs)-1].data.(events.Call)
}

// The sequence seen on a real phone: offer, then right away a reject from one of the
// account's own devices, then (seconds later) the real end of the call.
func TestRejectFromOwnDeviceDoesNotEndCall(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	defer h.Close()
	// Caller hung up before anyone answered: missed.
	h.WorkerEvent("i1", call("offer", "P1"))
	h.WorkerEvent("i1", selfReject("P1"))
	if got := r.types(); len(got) != 1 || got[0] != events.TypeCallReceived {
		t.Fatalf("own-device reject ended the call: %v", got)
	}
	h.WorkerEvent("i1", terminate("P1", ""))
	if e := r.last(); len(r.types()) != 2 || !e.Missed || e.Reason != "caller_hangup" {
		t.Fatalf("hung up = %v %+v", r.types(), e)
	}
	// Answered on the phone: the terminate says accepted_elsewhere, not missed.
	h.WorkerEvent("i1", call("offer", "P2"))
	h.WorkerEvent("i1", selfReject("P2"))
	h.WorkerEvent("i1", terminate("P2", "accepted_elsewhere"))
	if e := r.last(); e.CallID != "P2" || e.Missed || e.Reason != "accepted_elsewhere" {
		t.Fatalf("answered elsewhere = %+v", e)
	}
	// Answered with an accept from our own phone, then terminated.
	h.WorkerEvent("i1", call("offer", "P3"))
	h.WorkerEvent("i1", selfReject("P3"))
	h.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: "P3", Kind: "accept", FromSelf: true}}})
	h.WorkerEvent("i1", terminate("P3", ""))
	if e := r.last(); e.CallID != "P3" || e.Missed || e.Reason != "ended" {
		t.Fatalf("accepted = %+v", e)
	}
}

// Without a terminate the call ends after CallTimeout (reason timeout).
func TestCallTimeoutEndsCall(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	h.CallTimeout = 50 * time.Millisecond
	defer h.Close()
	h.WorkerEvent("i1", call("offer", "T1"))
	h.WorkerEvent("i1", selfReject("T1"))
	deadline := time.Now().Add(2 * time.Second)
	for len(r.types()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("call never timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := r.last(); e.Reason != "timeout" || !e.Missed || e.CallID != "T1" {
		t.Fatalf("timeout = %+v", e)
	}
	h.WorkerEvent("i1", terminate("T1", "")) // late terminate: no second call.ended
	time.Sleep(80 * time.Millisecond)
	if n := len(r.types()); n != 2 {
		t.Fatalf("events = %v", r.types())
	}
}

func hostedReject(id string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: id, Kind: "reject",
		Caller: "5511@s.whatsapp.net", FromHosted: true}}}
}

// The two real calls from the phone test, signal by signal.
func TestRealCallSequences(t *testing.T) {
	h, r := newHandler(settings.Defaults(), nil)
	h.RejectGrace = 20 * time.Millisecond // a hosted reject must not start the peer grace
	defer h.Close()

	// Call 1: offer, hosted reject 0.2s later, terminate without reason 7s later.
	h.WorkerEvent("i1", call("offer", "REAL1"))
	h.WorkerEvent("i1", hostedReject("REAL1"))
	time.Sleep(60 * time.Millisecond)
	if got := r.types(); len(got) != 1 {
		t.Fatalf("hosted reject ended the call: %v", got)
	}
	h.WorkerEvent("i1", terminate("REAL1", ""))
	if got := r.types(); len(got) != 2 || got[1] != events.TypeCallEnded {
		t.Fatalf("call 1 = %v", got)
	}
	if e := r.last(); !e.Missed || e.Reason != "caller_hangup" {
		t.Fatalf("call 1 ended = %+v", e)
	}

	// Call 2: offer, hosted reject, terminate accepted_elsewhere, accept from our phone.
	h.WorkerEvent("i1", call("offer", "REAL2"))
	h.WorkerEvent("i1", hostedReject("REAL2"))
	h.WorkerEvent("i1", terminate("REAL2", "accepted_elsewhere"))
	h.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Call{Call: &workerv1.Call{CallId: "REAL2", Kind: "accept",
		Caller: "5511@s.whatsapp.net", FromSelf: true}}})
	got := r.types()[2:]
	want := []string{events.TypeCallReceived, events.TypeCallAccepted, events.TypeCallEnded}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("call 2 = %v, want %v", got, want)
	}
	if e := r.last(); e.Missed || e.Reason != "accepted_elsewhere" || e.CallID != "REAL2" {
		t.Fatalf("call 2 ended = %+v", e)
	}
}
