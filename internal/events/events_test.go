package events

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, typ string
		want         bool
	}{
		{"*", "message.received", true},
		{"message.*", "message.received", true},
		{"message.*", "message.status", true},
		{"message.*", "instance.status", false},
		{"message.received", "message.received", true},
		{"message.received", "message.sent", false},
		{"message", "message.received", false},
		{"", "message.received", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.typ); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.typ, got, c.want)
		}
	}
	if !MatchAny([]string{"instance.*", "message.sent"}, "message.sent") || MatchAny(nil, "message.sent") {
		t.Fatal("MatchAny")
	}
}

func TestValidPattern(t *testing.T) {
	for _, ok := range []string{"*", "message.*", "message.received", "history.synced", "instance.*"} {
		if !ValidPattern(ok) {
			t.Errorf("ValidPattern(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "message", "nope.*", "message.nope", "mess*", "*.received"} {
		if ValidPattern(bad) {
			t.Errorf("ValidPattern(%q) = true", bad)
		}
	}
}

func TestPublishEnvelope(t *testing.T) {
	b := NewBus(nil)
	sub := b.Subscribe(Filter{}, 4)
	defer sub.Close()
	b.Publish("inst_1", TypeMessageReceived, map[string]string{"text": "oi"})
	e := <-sub.C()
	if !strings.HasPrefix(e.ID, "evt_") || e.Type != TypeMessageReceived || e.Version != 1 || e.InstanceID != "inst_1" {
		t.Fatalf("envelope = %+v", e)
	}
	var got map[string]any
	if err := json.Unmarshal(e.JSON, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "type", "version", "instance_id", "timestamp", "data"} {
		if _, ok := got[k]; !ok {
			t.Errorf("envelope JSON lacks %q: %s", k, e.JSON)
		}
	}
	if got["data"].(map[string]any)["text"] != "oi" {
		t.Fatalf("data = %v", got["data"])
	}
}

func TestSubscribeFilters(t *testing.T) {
	b := NewBus(nil)
	sub := b.Subscribe(Filter{InstanceID: "a", Types: []string{"message.*"}}, 8)
	defer sub.Close()
	b.Publish("b", TypeMessageReceived, nil)
	b.Publish("a", TypeInstanceStatus, nil)
	b.Publish("a", TypeMessageSent, nil)
	select {
	case e := <-sub.C():
		if e.InstanceID != "a" || e.Type != TypeMessageSent {
			t.Fatalf("got %s/%s", e.InstanceID, e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	select {
	case e := <-sub.C():
		t.Fatalf("unexpected %s/%s", e.InstanceID, e.Type)
	default:
	}
}

// A subscriber that does not keep up is dropped; Publish never blocks.
func TestSlowSubscriberIsDropped(t *testing.T) {
	b := NewBus(nil)
	slow := b.Subscribe(Filter{}, 2)
	fast := b.Subscribe(Filter{}, 100)
	defer fast.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			b.Publish("a", TypeMessageReceived, i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	n := 0
	for range slow.C() {
		n++
	}
	if n != 2 || !slow.Dropped() {
		t.Fatalf("slow got %d events, dropped=%v", n, slow.Dropped())
	}
	if len(fast.C()) != 10 {
		t.Fatalf("fast got %d", len(fast.C()))
	}
	if b.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", b.Subscribers())
	}
}

type recSink struct {
	mu  sync.Mutex
	evs []Envelope
}

func (r *recSink) Enqueue(e Envelope) { r.mu.Lock(); r.evs = append(r.evs, e); r.mu.Unlock() }

func TestSinksAndCloseSubscribers(t *testing.T) {
	sink := &recSink{}
	b := NewBus(nil, sink)
	sub := b.Subscribe(Filter{}, 4)
	b.Publish("a", TypeInstanceQR, nil)
	b.CloseSubscribers()
	if _, ok := <-sub.C(); !ok {
		t.Fatal("buffered event lost on close")
	}
	if _, ok := <-sub.C(); ok {
		t.Fatal("subscription still open")
	}
	if sub.Dropped() {
		t.Fatal("closed subscription reported as dropped")
	}
	b.Publish("a", TypeInstanceQR, nil) // sinks still get events after subscribers are closed
	if len(sink.evs) != 2 {
		t.Fatalf("sink got %d", len(sink.evs))
	}
	late := b.Subscribe(Filter{}, 1)
	if _, ok := <-late.C(); ok {
		t.Fatal("subscription after CloseSubscribers is open")
	}
	sub.Close() // idempotent
}

func TestHistoryTrackerDebounces(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	b := NewBus(fc)
	sub := b.Subscribe(Filter{}, 8)
	defer sub.Close()
	h := NewHistoryTracker(b, fc, 15*time.Second)
	h.Add("a", 10, 4)
	waitTimers(t, fc, 1)
	fc.Advance(10 * time.Second)
	h.Add("a", 5, 5)
	h.Add("b", 1, 0)
	waitTimers(t, fc, 1)
	fc.Advance(10 * time.Second) // a: 10s since its last chunk; b: 10s
	select {
	case e := <-sub.C():
		t.Fatalf("early event %s", e.JSON)
	case <-time.After(50 * time.Millisecond):
	}
	waitTimers(t, fc, 1)
	fc.Advance(5 * time.Second)
	got := map[string]HistorySynced{}
	for i := 0; i < 2; i++ {
		select {
		case e := <-sub.C():
			var env struct{ Data HistorySynced }
			if err := json.Unmarshal(e.JSON, &env); err != nil {
				t.Fatal(err)
			}
			got[e.InstanceID] = env.Data
		case <-time.After(2 * time.Second):
			t.Fatal("history.synced not published")
		}
	}
	if got["a"] != (HistorySynced{Chats: 2, Messages: 15, NewMessages: 9}) || got["b"] != (HistorySynced{Chats: 1, Messages: 1}) {
		t.Fatalf("got %+v", got)
	}
	h.Add("c", 3, 3)
	h.Close() // flushes pending counts
	select {
	case e := <-sub.C():
		if e.InstanceID != "c" || e.Type != TypeHistorySynced {
			t.Fatalf("got %s %s", e.InstanceID, e.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not flush")
	}
	h.Add("d", 1, 1) // after Close: ignored, no panic
}

func waitTimers(t *testing.T, fc *clock.Fake, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fc.Waiters() < n {
		if time.Now().After(deadline) {
			t.Fatalf("waiters = %d, want %d", fc.Waiters(), n)
		}
		time.Sleep(time.Millisecond)
	}
}
