package manager

import (
	"context"
	"testing"

	"github.com/douglasdf99/litezap/internal/workertest"
)

// A connection state with a detail reaches observers as a worker event (before the state
// change), so instance.proxy_failed can carry the worker's error text.
func TestConnDetailReachesObservers(t *testing.T) {
	m, _, sp := setup(t, "i1")
	obs := &reasonObserver{}
	m.SetObserver(obs)
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", func() bool { return obs.has("connected/") })
	sp.Last("i1").EmitConnDetail("reconnecting", "proxy_unreachable", "dial tcp: connection refused")
	eventually(t, "detail observed", func() bool {
		evs, _ := obs.snapshot()
		for _, ev := range evs {
			if c := ev.GetConn(); c != nil && c.Detail == "dial tcp: connection refused" && c.Reason == "proxy_unreachable" {
				return true
			}
		}
		return false
	})
	eventually(t, "state observed", func() bool { return obs.has("reconnecting/proxy_unreachable") })
	// Plain states are not forwarded as worker events.
	sp.Last("i1").Emit("connected")
	eventually(t, "reconnected", func() bool {
		_, sts := obs.snapshot()
		return len(sts) >= 3
	})
	evs, _ := obs.snapshot()
	n := 0
	for _, ev := range evs {
		if ev.GetConn() != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("conn worker events = %d, want 1", n)
	}
}
