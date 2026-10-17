package manager

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
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

func TestEmptyQRClearsCachedCode(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending"); f.EmitQR("QR-1") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	eventually(t, "qr code", func() bool { _, ok := m.QR("i1"); return ok })
	sp.Last("i1").EmitQR("") // pairing round ended
	eventually(t, "qr cleared", func() bool { _, ok := m.QR("i1"); return !ok })
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
	if in, _ := st.GetInstance(context.Background(), "i1"); in.JID != "" {
		t.Fatalf("JID must be cleared on logout, got %q", in.JID)
	}
	if !sp.Last("i1").Closed.Load() || sp.Starts("i1") != 1 {
		t.Fatal("worker should be closed once and not restarted")
	}
	if err := m.Logout(context.Background(), "i1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestLoggedOutFromPhoneClearsJID(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	m.Start(context.Background(), "i1")
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	eventually(t, "jid saved", func() bool { in, _ := st.GetInstance(context.Background(), "i1"); return in.JID != "" })
	sp.Last("i1").Emit("logged_out")
	eventually(t, "logged_out", stateIs(t, st, "i1", instance.LoggedOut))
	eventually(t, "jid cleared", func() bool { in, _ := st.GetInstance(context.Background(), "i1"); return in.JID == "" })
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
	if in, _ := st.GetInstance(context.Background(), "i1"); in.JID != "" {
		t.Fatalf("JID must be cleared on logout, got %q", in.JID)
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
	eventually(t, "connected after start", stateIs(t, st, "i1", instance.Connected))
	if !m.Running("i1") || sp.Starts("i1") != 2 {
		t.Fatalf("running = %v, starts = %d", m.Running("i1"), sp.Starts("i1"))
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

func TestRetireStopsAndBlocksStart(t *testing.T) {
	m, st, sp := setup(t, "i1")
	sp.OnStart = func(f *workertest.Fake) { f.Emit("qr_pending") }
	ctx := context.Background()
	if err := m.Start(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "qr_pending", stateIs(t, st, "i1", instance.QRPending))
	if err := m.Retire(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if sp.Live("i1") != 0 || m.Running("i1") {
		t.Fatalf("worker still alive after Retire: live=%d", sp.Live("i1"))
	}
	starts := sp.Starts("i1")
	if err := m.Start(ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Start after Retire = %v, want ErrNotFound", err)
	}
	time.Sleep(20 * time.Millisecond)
	if sp.Starts("i1") != starts || m.Running("i1") {
		t.Fatal("Start after Retire spawned a worker")
	}
}

func TestRetireWithoutWorkerBlocksStart(t *testing.T) {
	m, _, sp := setup(t, "i1")
	ctx := context.Background()
	if err := m.Retire(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Start after Retire = %v, want ErrNotFound", err)
	}
	if sp.Starts("i1") != 0 {
		t.Fatal("spawned after Retire")
	}
}

func TestUnretireAllowsStartAgain(t *testing.T) {
	m, _, sp := setup(t, "i1")
	ctx := context.Background()
	if err := m.Retire(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retired instance must not start, got %v", err)
	}
	m.Unretire("i1")
	if err := m.Start(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "worker started", func() bool { return sp.Starts("i1") == 1 })
}

type recObserver struct {
	mu     sync.Mutex
	events []*workerv1.Event
	states []instance.State
}

func (r *recObserver) WorkerEvent(_ string, ev *workerv1.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recObserver) StateChanged(_ string, st instance.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, st)
}

func (r *recObserver) snapshot() ([]*workerv1.Event, []instance.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*workerv1.Event(nil), r.events...), append([]instance.State(nil), r.states...)
}

func TestObserverGetsDataEventsAndStates(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &recObserver{}
	m.SetObserver(Observers{obs})
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	sp.Last("i1").EmitMessage(&workerv1.Message{Id: "W1", Chat: "a@s.whatsapp.net", Type: "text"})
	eventually(t, "message observed", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1 && evs[0].GetMessage().GetId() == "W1"
	})
	_, states := obs.snapshot()
	if len(states) < 2 || states[0] != instance.Starting || states[len(states)-1] != instance.Connected {
		t.Fatalf("states = %v", states)
	}
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.FirstConnectedAt.IsZero() {
		t.Fatal("first connection not recorded")
	}
	if cli, err := m.Client("i1"); err != nil || cli == nil {
		t.Fatalf("client = %v, %v", cli, err)
	}
	if _, err := m.Client("nope"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("client of unknown = %v", err)
	}
}

type blockingHistory struct {
	recObserver
	release chan struct{}
}

func (b *blockingHistory) WorkerEvent(id string, ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		<-b.release
	}
	b.recObserver.WorkerEvent(id, ev)
}

// A slow history write must not hold up state changes or live messages.
func TestHistoryDoesNotBlockStateAndLiveEvents(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &blockingHistory{release: make(chan struct{})}
	defer close(obs.release)
	m.SetObserver(obs)
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	f := sp.Last("i1")
	f.EmitHistory(&workerv1.HistoryChunk{Chat: "a@s.whatsapp.net"})
	f.EmitHistory(&workerv1.HistoryChunk{Chat: "b@s.whatsapp.net"})
	f.EmitMessage(&workerv1.Message{Id: "LIVE", Chat: "c@s.whatsapp.net"})
	f.Emit("reconnecting")
	eventually(t, "reconnecting while history is stuck", stateIs(t, st, "i1", instance.Reconnecting))
	eventually(t, "live message while history is stuck", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1 && evs[0].GetMessage().GetId() == "LIVE"
	})
}

// first_connected_at is only recorded when the connected state was actually persisted.
func TestFirstConnectedNeedsPersistedConnected(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &recObserver{}
	m.SetObserver(obs)
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "starting", stateIs(t, st, "i1", instance.Starting))
	// stopped -> connected is not an allowed transition: setState will refuse it.
	if err := st.UpdateInstanceState(context.Background(), "i1", instance.Stopped, ""); err != nil {
		t.Fatal(err)
	}
	f := sp.Last("i1")
	f.Emit("connected")
	f.EmitMessage(&workerv1.Message{Id: "AFTER", Chat: "a@s.whatsapp.net", Type: "text"})
	eventually(t, "message after connected", func() bool {
		evs, _ := obs.snapshot()
		return len(evs) == 1
	})
	in, _ := st.GetInstance(context.Background(), "i1")
	if in.State != instance.Stopped || !in.FirstConnectedAt.IsZero() {
		t.Fatalf("state=%s first=%v", in.State, in.FirstConnectedAt)
	}
}

// After Stop returns, the history consumer is done: nothing more is written for the worker.
func TestStopWaitsForHistoryWriter(t *testing.T) {
	m, st, sp := setup(t, "i1")
	obs := &slowHistory{}
	m.SetObserver(obs)
	sp.OnStart = func(f *workertest.Fake) { f.Emit("connected") }
	if err := m.Start(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "connected", stateIs(t, st, "i1", instance.Connected))
	f := sp.Last("i1")
	for i := 0; i < 5; i++ {
		f.EmitHistory(&workerv1.HistoryChunk{Chat: "a@s.whatsapp.net"})
	}
	eventually(t, "first chunk being written", func() bool { return obs.count() >= 1 })
	if err := m.Stop(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	n := obs.count()
	time.Sleep(300 * time.Millisecond)
	if got := obs.count(); got != n {
		t.Fatalf("history written after Stop returned: %d -> %d", n, got)
	}
}

type slowHistory struct {
	mu sync.Mutex
	n  int
}

func (s *slowHistory) WorkerEvent(_ string, ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		time.Sleep(100 * time.Millisecond)
		s.mu.Lock()
		s.n++
		s.mu.Unlock()
	}
}
func (s *slowHistory) StateChanged(string, instance.State) {}
func (s *slowHistory) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
