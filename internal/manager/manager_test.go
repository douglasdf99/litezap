package manager

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
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
	return setupWith(t, testOptions(), ids...)
}

func setupWith(t *testing.T, opt Options, ids ...string) (*Manager, *store.Store, *workertest.Spawner) {
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
	m := New(st, sp, opt, slog.New(slog.DiscardHandler))
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

func TestLogoutWithMissedEventEndsSupervision(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.WA.SilentLogout.Store(true); f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	begin := time.Now()
	if err := m.Logout(ctx, "i1"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("logout took %s", d)
	}
	if !stateIs(t, st, "i1", instance.LoggedOut)() {
		t.Fatal("state must be logged_out when Logout returns")
	}
	if m.Running("i1") {
		t.Fatal("must not be running after logout")
	}
	time.Sleep(50 * time.Millisecond)
	if n := sp.Starts("i1"); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	if sp.Live("i1") != 0 {
		t.Fatal("worker must be closed after logout")
	}
}

func TestStartDuringSlowStopWaitsForOldWorker(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.CloseDelay = 200 * time.Millisecond; f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(ctx, "i1") }()
	eventually(t, "close begun", func() bool { return sp.Last("i1").Closed.Load() })
	if err := m.Start(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if n := sp.MaxLive("i1"); n > 1 {
		t.Fatalf("%d workers alive at once", n)
	}
}

func TestConcurrentStopStartNeverTwoWorkers(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.CloseDelay = 20 * time.Millisecond; f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	for range 10 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = m.Stop(ctx, "i1") }()
		go func() { defer wg.Done(); _ = m.Start(ctx, "i1") }()
		wg.Wait()
		if n := sp.MaxLive("i1"); n > 1 {
			t.Fatalf("%d workers alive at once", n)
		}
	}
}

func TestWorkerReportedTerminalStates(t *testing.T) {
	for _, want := range []instance.State{instance.Banned, instance.Failed} {
		t.Run(string(want), func(t *testing.T) {
			m, st, sp := setup(t, "i1")
			sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
			m.Start(context.Background(), "i1")
			eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
			sp.Last("i1").Emit(string(want))
			eventually(t, string(want), stateIs(t, st, "i1", want))
			eventually(t, "not running", func() bool { return !m.Running("i1") })
			time.Sleep(50 * time.Millisecond)
			if n := sp.Starts("i1"); n != 1 {
				t.Fatalf("starts = %d, want 1", n)
			}
			if !stateIs(t, st, "i1", want)() {
				t.Fatal("terminal state must persist")
			}
		})
	}
}

func TestStopDuringBackoff(t *testing.T) {
	opt := testOptions()
	opt.BackoffBase, opt.BackoffMax, opt.BreakerMax = 10*time.Second, 10*time.Second, 100
	m, st, sp := setupWith(t, opt, "i1")
	sp.FailStart = errors.New("spawn failed")
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "starting", stateIs(t, st, "i1", instance.Starting))
	time.Sleep(20 * time.Millisecond) // let the failed spawn enter backoff
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := m.Stop(sctx, "i1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stateIs(t, st, "i1", instance.Stopped)() || m.Running("i1") {
		t.Fatal("instance should be stopped and not running")
	}
}

func TestStartTwiceIsNoop(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	m.Start(ctx, "i1")
	time.Sleep(50 * time.Millisecond)
	if n := sp.Starts("i1"); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
}

func TestShutdownKeepsStateAndRefusesStart(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	ctx := context.Background()
	m.Start(ctx, "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	m.Shutdown(ctx)
	if m.Running("i1") || sp.Live("i1") != 0 {
		t.Fatal("worker should be closed after shutdown")
	}
	if !stateIs(t, st, "i1", instance.Connected)() {
		t.Fatal("shutdown must keep the stored state")
	}
	if err := m.Start(ctx, "i1"); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("expected ErrShuttingDown, got %v", err)
	}
}
