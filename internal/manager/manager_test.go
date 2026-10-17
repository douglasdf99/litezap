package manager

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/workertest"
)

func testOptions() Options {
	return Options{
		HeartbeatInterval: 30 * time.Millisecond,
		HeartbeatFailures: 2,
		BackoffBase:       time.Millisecond,
		BackoffMax:        5 * time.Millisecond,
		BreakerMax:        3,
		BreakerWindow:     time.Minute,
		StopTimeout:       time.Second,
	}
}

func setup(t *testing.T, ids ...string) (*Manager, *store.Store, *workertest.Spawner) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range ids {
		if err := st.CreateInstance(context.Background(), store.Instance{ID: id, Name: id, State: instance.Created, TokenHash: "h-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	sp := workertest.NewSpawner()
	m := New(st, sp, testOptions(), slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})
	return m, st, sp
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stateIs(t *testing.T, st *store.Store, id string, want instance.State) func() bool {
	return func() bool {
		in, err := st.GetInstance(context.Background(), id)
		return err == nil && in.State == want
	}
}

func TestStartReachesQRPendingAndExposesQR(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-1") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	eventually(t, "qr code", func() bool { c, ok := m.QR("i1"); return ok && c == "QR-1" })

	sp.Last("i1").Emit("connected")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if _, ok := m.QR("i1"); ok {
		t.Fatal("QR must be cleared after connect")
	}
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.JID == "" {
		t.Fatal("JID not saved")
	}
}

func TestCrashRestartsWorker(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connecting"); f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	sp.Last("i1").Crash()
	eventually(t, "second start", func() bool { return sp.Starts("i1") == 2 })
	eventually(t, "connected again", stateIs(t, st, "i1", instance.Connected))
}

func TestCrashLoopOpensBreaker(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Crash() }
	m.Start(context.Background(), "i1")
	eventually(t, "failed", stateIs(t, st, "i1", instance.Failed))
	eventually(t, "not running", func() bool { return !m.Running("i1") })
	if n := sp.Starts("i1"); n != 3 {
		t.Fatalf("starts = %d, want 3", n)
	}
}

func TestHeartbeatFailureKillsAndRestarts(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) {
		if sp.Starts("i1") == 1 {
			f.PingBlock.Store(true)
		}
		f.Emit("connected")
	}
	m.Start(context.Background(), "i1")
	eventually(t, "restart after heartbeat failure", func() bool { return sp.Starts("i1") == 2 })
	select {
	case <-sp.Nth("i1", 0).Done():
	default:
		t.Fatal("hung worker should have been killed")
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
}

func TestLogoutIsTerminal(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Logout(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "logged_out", stateIs(t, st, "i1", instance.LoggedOut))
	eventually(t, "not running", func() bool { return !m.Running("i1") })
	if !sp.Last("i1").Closed.Load() || sp.Starts("i1") != 1 {
		t.Fatal("worker should be closed once and not restarted")
	}
	if err := m.Logout(context.Background(), "i1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestStartRightAfterLogoutRestarts(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Logout(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second start", func() bool { return sp.Starts("i1") == 2 })
	eventually(t, "connected again", stateIs(t, st, "i1", instance.Connected))
}

func TestStopAndRestart(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	if err := m.Stop(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped", stateIs(t, st, "i1", instance.Stopped))
	if m.Running("i1") || !sp.Last("i1").Closed.Load() {
		t.Fatal("worker should be closed after stop")
	}
	if err := m.Restart(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected after restart", stateIs(t, st, "i1", instance.Connected))
	if sp.Starts("i1") != 2 {
		t.Fatalf("starts = %d", sp.Starts("i1"))
	}
}

func TestBootStartsOnlyActiveInstances(t *testing.T) {
	m, st, sp := setup(t, "on", "off", "out")
	ctx := context.Background()
	st.UpdateInstanceState(ctx, "on", instance.Connected, "")
	st.UpdateInstanceState(ctx, "off", instance.Stopped, "")
	st.UpdateInstanceState(ctx, "out", instance.LoggedOut, "")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "on started", func() bool { return sp.Starts("on") == 1 })
	time.Sleep(50 * time.Millisecond)
	if sp.Starts("off") != 0 || sp.Starts("out") != 0 {
		t.Fatal("stopped/logged_out instances must not boot")
	}
}

func TestPairPhone(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending") }
	m.Start(context.Background(), "i1")
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	code, err := m.PairPhone(context.Background(), "i1", "5511999999999")
	if err != nil || code != "PAIR-CODE" {
		t.Fatalf("code = %q, %v", code, err)
	}
}

func TestStartRefusesBanned(t *testing.T) {
	m, st, _ := setup(t, "i1")
	st.UpdateInstanceState(context.Background(), "i1", instance.Banned, "")
	if err := m.Start(context.Background(), "i1"); !errors.Is(err, ErrBanned) {
		t.Fatalf("expected ErrBanned, got %v", err)
	}
}
