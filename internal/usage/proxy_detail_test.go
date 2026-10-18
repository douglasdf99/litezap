package usage

import (
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/instance"
)

func TestProxyWatchCarriesWorkerError(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	pub := &recorded{}
	w := &ProxyWatch{Bus: pub, Now: fc.Now, Repeat: time.Minute}
	conn := func(reason, detail string) *workerv1.Event {
		return &workerv1.Event{Kind: &workerv1.Event_Conn{Conn: &workerv1.ConnState{State: "reconnecting", Reason: reason, Detail: detail}}}
	}
	w.WorkerEvent("i1", conn(ProxyUnreachable, "dial tcp 10.0.0.1:1080: connection refused"))
	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable)
	if len(pub.evts) != 1 || pub.evts[0].Error != "dial tcp 10.0.0.1:1080: connection refused" {
		t.Fatalf("first notice = %+v", pub.evts)
	}
	// A newer error shows up in the reminder; other reasons are ignored.
	w.WorkerEvent("i1", conn(ProxyUnreachable, "i/o timeout"))
	w.WorkerEvent("i1", conn("connect_failed", "other"))
	fc.Advance(time.Minute)
	w.Tick()
	if len(pub.evts) != 2 || pub.evts[1].Error != "i/o timeout" {
		t.Fatalf("reminder = %+v", pub.evts)
	}
	// Recovery forgets it.
	w.StateChanged("i1", instance.Connected, "")
	w.StateChanged("i1", instance.Reconnecting, ProxyUnreachable)
	if pub.evts[2].Error != "" {
		t.Fatalf("stale error = %+v", pub.evts[2])
	}
}
