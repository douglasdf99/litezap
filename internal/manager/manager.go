// Package manager supervises instance workers: lifecycle, heartbeat, restart and circuit breaker.
package manager

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/store"
)

var (
	ErrNotRunning   = errors.New("instance worker is not running")
	ErrBanned       = errors.New("instance is banned")
	ErrShuttingDown = errors.New("manager is shutting down")
)

// Observer receives worker data events (messages, receipts, history, QR codes) and every
// persisted state change. Calls are synchronous, sometimes with the Manager's lock held: they must
// be quick and must never call into the Manager. History chunks arrive on a separate
// goroutine, concurrently with the other calls, so observers must be safe for concurrent use.
type Observer interface {
	WorkerEvent(instanceID string, ev *workerv1.Event)
	StateChanged(instanceID string, st instance.State, reason string)
}

// Observers fans out to several observers, in order.
type Observers []Observer

func (obs Observers) WorkerEvent(id string, ev *workerv1.Event) {
	for _, o := range obs {
		o.WorkerEvent(id, ev)
	}
}

func (obs Observers) StateChanged(id string, st instance.State, reason string) {
	for _, o := range obs {
		o.StateChanged(id, st, reason)
	}
}

type nopObserver struct{}

func (nopObserver) WorkerEvent(string, *workerv1.Event)         {}
func (nopObserver) StateChanged(string, instance.State, string) {}

type Options struct {
	HeartbeatInterval time.Duration
	HeartbeatFailures int
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	BreakerMax        int
	BreakerWindow     time.Duration
	StopTimeout       time.Duration
}

// DefaultOptions follows spec section 9.2.
func DefaultOptions() Options {
	return Options{
		HeartbeatInterval: 10 * time.Second,
		HeartbeatFailures: 3,
		BackoffBase:       time.Second,
		BackoffMax:        5 * time.Minute,
		BreakerMax:        5,
		BreakerWindow:     10 * time.Minute,
		StopTimeout:       10 * time.Second,
	}
}

// Manager supervises one worker per instance.
//
// Locking: supervise's cleanup writes the final state to the store while holding m.mu
// (and Stop does the same when no supervisor runs). Callers must never hold a store
// transaction while calling into the Manager, or they can deadlock against that write.
type Manager struct {
	store    *store.Store
	spawner  spawner.Spawner
	opt      Options
	log      *slog.Logger
	observer Observer

	mu      sync.Mutex
	running map[string]*supervised
	closed  bool // set by Shutdown; Start refuses afterwards
	// retired holds instances being deleted: Start refuses them (as not found), so a
	// Start racing a Delete cannot spawn a worker for a row that is about to vanish.
	retired map[string]bool
}

type supervised struct {
	id      string
	cancel  context.CancelFunc
	done    chan struct{}
	exiting atomic.Bool // supervise is returning; a new Start must wait for done
	breaker Breaker

	// Final state requests, read by supervise's cleanup. Set under Manager.mu while the
	// entry is still in the map, so the cleanup either sees them or the caller sees no entry.
	logoutRequested atomic.Bool
	stopRequested   atomic.Bool

	mu     sync.Mutex
	handle spawner.WorkerHandle
	qr     string
}

func (s *supervised) setHandle(h spawner.WorkerHandle) { s.mu.Lock(); s.handle = h; s.mu.Unlock() }
func (s *supervised) getHandle() spawner.WorkerHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handle
}
func (s *supervised) setQR(c string) { s.mu.Lock(); s.qr = c; s.mu.Unlock() }
func (s *supervised) getQR() string  { s.mu.Lock(); defer s.mu.Unlock(); return s.qr }

func New(st *store.Store, sp spawner.Spawner, opt Options, log *slog.Logger) *Manager {
	return &Manager{store: st, spawner: sp, opt: opt, log: log, observer: nopObserver{},
		running: map[string]*supervised{}, retired: map[string]bool{}}
}

// SetObserver registers the observer of worker events and state changes. Call it before
// Boot or Start; it is not safe to change while instances run.
func (m *Manager) SetObserver(o Observer) { m.observer = o }

// Start begins supervising an instance; it is a no-op if already running.
func (m *Manager) Start(ctx context.Context, id string) error {
	in, err := m.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	if in.State == instance.Banned {
		return ErrBanned
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if m.closed {
			return ErrShuttingDown
		}
		if m.retired[id] {
			return store.ErrNotFound
		}
		old, ok := m.running[id]
		if !ok {
			break
		}
		if !old.exiting.Load() {
			return nil
		}
		// The previous supervisor is shutting down (e.g. just logged out): wait for it.
		m.mu.Unlock()
		select {
		case <-old.done:
		case <-ctx.Done():
			m.mu.Lock()
			return ctx.Err()
		}
		m.mu.Lock()
	}
	sctx, cancel := context.WithCancel(context.Background())
	s := &supervised{
		id: id, cancel: cancel, done: make(chan struct{}),
		breaker: Breaker{Max: m.opt.BreakerMax, Window: m.opt.BreakerWindow},
	}
	m.running[id] = s
	go m.supervise(sctx, s)
	return nil
}

// halt stops supervision and waits for the worker to exit, without changing the stored state.
// The entry stays in the map until supervise's cleanup removes it, so a concurrent Start
// waits for the old worker instead of spawning a second one on the same session.
func (m *Manager) halt(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return m.end(ctx, s)
}

// end asks a supervisor to exit and waits for it.
func (m *Manager) end(ctx context.Context, s *supervised) error {
	s.exiting.Store(true)
	s.cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if _, err := m.store.GetInstance(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	s := m.running[id]
	if s == nil {
		// Under m.mu so a concurrent Start cannot slip a supervisor in before the write.
		m.setState(id, instance.Stopped, "")
		m.mu.Unlock()
		return nil
	}
	s.stopRequested.Store(true) // supervise's cleanup persists stopped
	s.exiting.Store(true)       // under m.mu, so a concurrent Start waits instead of no-op
	m.mu.Unlock()
	return m.end(ctx, s)
}

// Retire permanently stops an instance that is being deleted: after it returns, the
// worker is gone and Start refuses the id with store.ErrNotFound.
func (m *Manager) Retire(ctx context.Context, id string) error {
	m.mu.Lock()
	m.retired[id] = true
	m.mu.Unlock()
	return m.Stop(ctx, id)
}

// Unretire undoes Retire, for a Delete that failed after the worker was stopped.
func (m *Manager) Unretire(id string) {
	m.mu.Lock()
	delete(m.retired, id)
	m.mu.Unlock()
}

func (m *Manager) Restart(ctx context.Context, id string) error {
	if err := m.halt(ctx, id); err != nil {
		return err
	}
	return m.Start(ctx, id)
}

// Shutdown halts every worker but keeps stored states, so Boot resumes them.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.halt(ctx, id) }()
	}
	wg.Wait()
}

// Boot starts every instance that was not stopped or waiting for human action.
func (m *Manager) Boot(ctx context.Context) error {
	list, err := m.store.ListInstances(ctx)
	if err != nil {
		return err
	}
	for _, in := range list {
		if in.State == instance.Created || in.State.Active() {
			if err := m.Start(ctx, in.ID); err != nil {
				m.log.Error("boot instance", "instance_id", in.ID, "err", err)
			}
		}
	}
	return nil
}

func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

func (m *Manager) handle(id string) (spawner.WorkerHandle, error) {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return nil, ErrNotRunning
	}
	h := s.getHandle()
	if h == nil {
		return nil, ErrNotRunning
	}
	return h, nil
}

// Client returns the gRPC client of the instance's running worker (ErrNotRunning otherwise).
func (m *Manager) Client(id string) (workerv1.WorkerClient, error) {
	h, err := m.handle(id)
	if err != nil {
		return nil, err
	}
	return h.Client(), nil
}

// QR returns the latest QR code while the instance is waiting to be paired.
func (m *Manager) QR(id string) (string, bool) {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return "", false
	}
	c := s.getQR()
	return c, c != ""
}

func (m *Manager) PairPhone(ctx context.Context, id, phone string) (string, error) {
	h, err := m.handle(id)
	if err != nil {
		return "", err
	}
	resp, err := h.Client().PairPhone(ctx, &workerv1.PairPhoneRequest{Phone: phone})
	if err != nil {
		return "", err
	}
	return resp.Code, nil
}

// Logout unlinks the number and ends supervision. The worker normally reports
// logged_out itself; if that event is missed, supervise's cleanup persists it.
// It blocks until the worker has been closed (up to about Options.StopTimeout),
// so a following Start begins a fresh worker.
func (m *Manager) Logout(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.running[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNotRunning
	}
	h := s.getHandle()
	if h == nil {
		return ErrNotRunning
	}
	if _, err := h.Client().Logout(ctx, &workerv1.LogoutRequest{}); err != nil {
		return err
	}
	m.mu.Lock()
	if m.running[s.id] == s {
		s.logoutRequested.Store(true)
		s.exiting.Store(true) // under m.mu, so a concurrent Start waits instead of no-op
	}
	m.mu.Unlock()
	return m.end(ctx, s)
}

func (m *Manager) supervise(ctx context.Context, s *supervised) {
	defer func() {
		s.exiting.Store(true)
		m.mu.Lock()
		// Persist the requested final state before removing the entry: a concurrent Start
		// waits for done, so its new supervisor cannot be overwritten by this write.
		switch {
		case s.stopRequested.Load():
			m.setState(s.id, instance.Stopped, "")
		case s.logoutRequested.Load():
			if in, err := m.store.GetInstance(context.Background(), s.id); err == nil && in.State != instance.LoggedOut {
				m.setState(s.id, instance.LoggedOut, "api") // the worker's event was missed
			}
		}
		if m.running[s.id] == s {
			delete(m.running, s.id)
		}
		m.mu.Unlock()
		close(s.done)
	}()
	attempt := 0
	for ctx.Err() == nil && !s.exiting.Load() {
		m.setState(s.id, instance.Starting, "")
		h, err := m.spawner.Start(ctx, s.id)
		if err != nil {
			if ctx.Err() != nil {
				return // a cancelled spawn is not a crash
			}
			m.log.Error("spawn worker", "instance_id", s.id, "err", err)
		} else {
			s.setHandle(h)
			terminal, healthy := m.runWorker(ctx, s, h)
			s.setHandle(nil)
			s.setQR("")
			if terminal || ctx.Err() != nil || s.exiting.Load() {
				return
			}
			if healthy {
				attempt = 0
			}
		}
		if s.breaker.Record(time.Now()) {
			s.exiting.Store(true)
			m.log.Error("worker crash loop; giving up", "instance_id", s.id)
			m.setState(s.id, instance.Failed, "crash_loop")
			return
		}
		d := Backoff(attempt, m.opt.BackoffBase, m.opt.BackoffMax)
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// runWorker consumes worker events and heartbeats until the worker ends.
// terminal: supervision must stop. healthy: the worker reached connected.
func (m *Manager) runWorker(ctx context.Context, s *supervised, h spawner.WorkerHandle) (terminal, healthy bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	closeWorker := func() {
		cancel() // end our Events stream first so the worker's graceful stop is not held open
		cctx, c := context.WithTimeout(context.Background(), m.opt.StopTimeout)
		defer c()
		_ = h.Close(cctx)
	}

	stream, err := h.Client().Events(sctx, &workerv1.EventsRequest{})
	if err != nil {
		m.log.Warn("subscribe worker events", "instance_id", s.id, "err", err)
		_ = h.Kill()
		return false, false
	}
	evs := make(chan *workerv1.Event)
	errc := make(chan error, 1)
	// History chunks are written by their own goroutine, one at a time, so a large history
	// sync never holds up state changes and heartbeats; live events wait at most for the
	// chunk being written (the worker already sends live events before queued chunks).
	hist := make(chan *workerv1.Event, 1)
	var histDone sync.WaitGroup
	histDone.Add(1)
	go func() {
		defer histDone.Done()
		for ev := range hist {
			m.observer.WorkerEvent(s.id, ev)
		}
	}()
	// Once runWorker returns nothing more is written for this worker: cancel ends the
	// reader, which closes hist, and we wait for the writer to finish its current chunk.
	defer func() {
		cancel()
		histDone.Wait()
	}()
	go func() {
		defer close(hist)
		for {
			ev, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			out := evs
			if ev.GetHistory() != nil {
				out = hist
			}
			select {
			case out <- ev:
			case <-sctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(m.opt.HeartbeatInterval)
	defer tick.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			s.exiting.Store(true)
			closeWorker()
			return true, healthy
		case <-h.Done():
			m.log.Warn("worker exited", "instance_id", s.id)
			return false, healthy
		case err := <-errc:
			m.log.Warn("worker event stream ended", "instance_id", s.id, "err", err)
			_ = h.Kill()
			return false, healthy
		case ev := <-evs:
			if qr := ev.GetQr(); qr != nil {
				s.setQR(qr.Code) // an empty code clears it: the worker's pairing round ended
				m.observer.WorkerEvent(s.id, ev)
				continue
			}
			c := ev.GetConn()
			if c == nil {
				m.observer.WorkerEvent(s.id, ev) // message or receipt
				continue
			}
			st := instance.State(c.State)
			if st != instance.QRPending {
				s.setQR("")
			}
			if st == instance.Connected {
				healthy = true
				if c.Jid != "" {
					if err := m.store.SetInstanceJID(context.Background(), s.id, c.Jid); err != nil {
						m.log.Error("save jid", "instance_id", s.id, "err", err)
					}
				}
			}
			persisted := m.setState(s.id, st, c.Reason)
			if persisted && st == instance.Connected {
				if err := m.store.MarkFirstConnected(context.Background(), s.id, time.Now()); err != nil {
					m.log.Error("save first connection", "instance_id", s.id, "err", err)
				}
			}
			if st.Terminal() {
				s.exiting.Store(true) // before the (possibly slow) close, so a concurrent Start waits
				closeWorker()
				return true, healthy
			}
		case <-tick.C:
			pctx, pc := context.WithTimeout(ctx, m.opt.HeartbeatInterval)
			_, err := h.Client().Ping(pctx, &workerv1.PingRequest{})
			pc()
			if err != nil {
				fails++
			} else {
				fails = 0
			}
			if fails >= m.opt.HeartbeatFailures {
				m.log.Warn("worker heartbeat failed; killing", "instance_id", s.id, "failures", fails)
				_ = h.Kill()
				return false, healthy
			}
		}
	}
}

// setState persists a transition if the state machine allows it and reports whether it
// wrote one. Persisting logged_out also clears the instance's JID.
func (m *Manager) setState(id string, to instance.State, reason string) bool {
	ctx := context.Background()
	in, err := m.store.GetInstance(ctx, id)
	if err != nil {
		m.log.Error("load instance for state change", "instance_id", id, "err", err)
		return false
	}
	if in.State == to && in.StateReason == reason {
		return false
	}
	if !instance.CanTransition(in.State, to) {
		m.log.Warn("invalid state transition ignored", "instance_id", id, "from", in.State, "to", to)
		return false
	}
	if err := m.store.UpdateInstanceState(ctx, id, to, reason); err != nil {
		m.log.Error("update state", "instance_id", id, "err", err)
		return false
	}
	if to == instance.LoggedOut {
		// The number is no longer linked: drop it so the API stops showing a phone.
		if err := m.store.SetInstanceJID(ctx, id, ""); err != nil {
			m.log.Error("clear jid", "instance_id", id, "err", err)
		}
	}
	m.log.Info("instance state", "instance_id", id, "from", in.State, "to", to, "reason", reason)
	m.observer.StateChanged(id, to, reason)
	return true
}
