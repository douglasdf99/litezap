package usage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRecorderCountsAndFlushes(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fc := clock.NewFake(time.Date(2026, 10, 17, 23, 59, 0, 0, time.UTC))
	rec := &Recorder{Store: st, Now: fc.Now}
	bus := events.NewBus(fc, rec)

	bus.Publish("i1", events.TypeMessageSent, events.Message{Source: "api", Type: "image", Media: &events.Media{Size: 1000}})
	bus.Publish("i1", events.TypeMessageSent, events.Message{Source: "device", Type: "text"}) // not billed
	bus.Publish("i1", events.TypeMessageReceived, events.Message{Source: "contact", Type: "audio", Media: &events.Media{Size: 50}})
	bus.Publish("i1", events.TypeInstanceStatus, events.InstanceStatus{State: instance.Connected})
	fc.Advance(2 * time.Minute) // crosses midnight
	bus.Publish("i1", events.TypeInstanceStatus, events.InstanceStatus{State: instance.Reconnecting, Reason: "disconnected"})
	bus.Publish("i1", events.TypeInstanceStatus, events.InstanceStatus{State: instance.Connected})
	fc.Advance(30 * time.Second)

	rows, err := rec.Usage(ctx, "2026-10-17", "2026-10-18", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("usage = %+v, %v", rows, err)
	}
	d1, d2 := rows[0], rows[1]
	if d1.MessagesSent != 1 || d1.MessagesReceived != 1 || d1.MediaBytes != 1050 || d1.ConnectedSeconds != 60 {
		t.Fatalf("day 1 = %+v", d1)
	}
	if d2.ConnectedSeconds != 90 { // 60s before the drop + 30s of the ongoing connection
		t.Fatalf("day 2 = %+v", d2)
	}
	// A second flush does not count the same time twice.
	fc.Advance(10 * time.Second)
	rows, _ = rec.Usage(ctx, "2026-10-18", "2026-10-18", "i1")
	if rows[0].ConnectedSeconds != 100 {
		t.Fatalf("after another flush = %+v", rows[0])
	}
	c := rec.Counters()
	if c.MessagesSent != 1 || c.MessagesReceived != 1 || c.MediaBytes != 1050 || c.StateChanges["connected"] != 2 {
		t.Fatalf("counters = %+v", c)
	}
	if err := rec.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type recorded struct {
	mu   sync.Mutex
	evts []events.InstanceProxyFailed
}

func (r *recorded) Publish(_ string, typ string, data any) {
	if typ != events.TypeInstanceProxyFailed {
		return
	}
	b, _ := json.Marshal(data)
	var e events.InstanceProxyFailed
	_ = json.Unmarshal(b, &e)
	r.mu.Lock()
	r.evts = append(r.evts, e)
	r.mu.Unlock()
}

func TestProxyWatch(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	pub := &recorded{}
	w := &ProxyWatch{Bus: pub, Now: fc.Now, Repeat: 5 * time.Minute}

	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable)
	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable) // same outage
	if len(pub.evts) != 1 || pub.evts[0].Repeat != 0 || pub.evts[0].OutagesLastHour != 1 {
		t.Fatalf("first notice = %+v", pub.evts)
	}
	fc.Advance(4 * time.Minute)
	w.Tick()
	if len(pub.evts) != 1 {
		t.Fatal("repeated too early")
	}
	fc.Advance(time.Minute)
	w.Tick()
	if len(pub.evts) != 2 || pub.evts[1].Repeat != 1 || pub.evts[1].DurationSeconds != 300 {
		t.Fatalf("reminder = %+v", pub.evts)
	}
	if w.Unreachable() != 1 {
		t.Fatal("outage not tracked")
	}
	w.StateChanged("i1", instance.Connected, "")
	fc.Advance(10 * time.Minute)
	w.Tick()
	if len(pub.evts) != 2 || w.Unreachable() != 0 {
		t.Fatal("reminder after recovery")
	}
	w.StateChanged("i1", instance.QRPending, "")
	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable)
	if n := len(pub.evts); n != 3 || pub.evts[2].OutagesLastHour != 2 {
		t.Fatalf("second outage = %+v", pub.evts)
	}
	fc.Advance(2 * time.Hour)
	w.StateChanged("i1", instance.Connected, "")
	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable)
	if pub.evts[3].OutagesLastHour != 1 || w.Failures() != 3 {
		t.Fatalf("old outages still counted: %+v failures=%d", pub.evts[3], w.Failures())
	}
}
