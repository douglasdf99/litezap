package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Whatsmeow adapts a whatsmeow client to WAClient.
//
// Pairing deliberately does not use whatsmeow's GetQRChannel: its emitter cannot be torn
// down safely. Instead handle() reacts to events.QR by rotating the codes itself, and
// pairLoop owns connecting/disconnecting. Nothing here ever blocks the event handler.
type Whatsmeow struct {
	cli  *whatsmeow.Client
	log  *slog.Logger
	sink Sink
	// connect is cli.ConnectContext; replaced in tests.
	connect func(context.Context) error

	// Signals to pairLoop; all are buffered (1) and sent to without blocking.
	restart    chan struct{} // from PairPhone: end the current round now
	roundEnded chan struct{} // codes exhausted, or unpaired socket dropped
	paired     chan struct{} // events.PairSuccess
	terminalCh chan struct{} // a logged_out/banned/failed state was reported
	firstShown chan struct{} // the current round showed its first code
	rotate     chan *events.RotateADVSecret

	terminal   atomic.Bool // set before reporting logged_out/banned/failed
	accepting  atomic.Bool // pairLoop is connected and wants roundEnded signals
	connecting atomic.Bool // a connectLoop is running; at most one at a time

	mu         sync.Mutex
	loopCtx    context.Context
	cancel     context.CancelFunc
	loopDone   chan struct{} // closed when pairLoop exits (nil if never started)
	pairing    bool          // pairLoop is active
	roundStart time.Time     // when the current round's first code was shown
	ready      chan struct{} // closed when the current round delivered its first code
	delivered  bool          // a code was delivered since pairLoop's last connect
	rotCancel  context.CancelFunc
	rotDone    chan struct{}
}

func NewWhatsmeow(cli *whatsmeow.Client, log *slog.Logger) *Whatsmeow {
	return &Whatsmeow{
		cli: cli, log: log, connect: cli.ConnectContext,
		restart:    make(chan struct{}, 1),
		roundEnded: make(chan struct{}, 1),
		paired:     make(chan struct{}, 1),
		terminalCh: make(chan struct{}, 1),
		firstShown: make(chan struct{}, 1),
		rotate:     make(chan *events.RotateADVSecret, 4),
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func drain(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

func (w *Whatsmeow) Run(ctx context.Context, sink Sink) error {
	w.sink = sink
	w.cli.AddEventHandler(w.handle)
	loopCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.loopCtx, w.cancel = loopCtx, cancel
	w.mu.Unlock()
	if w.cli.Store.ID != nil {
		sink.ConnState("connecting", "", w.jid())
		w.startConnectLoop(loopCtx, false)
		return nil
	}
	w.mu.Lock()
	w.pairing = true
	w.loopDone = make(chan struct{})
	w.mu.Unlock()
	go w.pairLoop(loopCtx)
	return nil
}

func (w *Whatsmeow) stopLoops() {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// sleep waits for d or ctx; it reports whether the full delay elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d *= 2; d > time.Minute {
		return time.Minute
	}
	return d
}

// startConnectLoop runs connectLoop in the background unless one is already running.
// With dropFirst, the current (failed) socket is dropped first.
func (w *Whatsmeow) startConnectLoop(ctx context.Context, dropFirst bool) {
	if !w.connecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer w.connecting.Store(false)
		if dropFirst {
			w.cli.Disconnect()
		}
		w.connectLoop(ctx)
	}()
}

// connectLoop retries the initial connection (e.g. no network at boot) instead of
// exiting. With whatsmeow's InitialAutoReconnect enabled, Connect returns nil and
// retries internally, so this loop simply ends. Without it, we retry here instead of
// exiting, which would trip the supervisor's circuit breaker. Once connected,
// whatsmeow's own auto-reconnect takes over.
func (w *Whatsmeow) connectLoop(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil && !w.terminal.Load() {
		err := w.connect(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		w.log.Warn("connect failed; retrying", "err", err, "in", delay)
		w.sink.ConnState("reconnecting", "connect_failed", w.jid())
		if !sleep(ctx, delay) {
			return
		}
		delay = nextBackoff(delay)
	}
}

// pairLoop connects an unpaired client and keeps it offering QR codes (rotated by
// handle -> startRotation) until pairing succeeds, a terminal state is reported or ctx ends.
func (w *Whatsmeow) pairLoop(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.pairing = false
		done := w.loopDone
		w.mu.Unlock()
		w.stopRotation()
		if done != nil {
			close(done)
		}
	}()
	backoff := time.Second
	for ctx.Err() == nil && !w.terminal.Load() {
		drain(w.roundEnded)
		drain(w.restart)
		drain(w.firstShown)
		w.mu.Lock()
		w.delivered = false
		w.mu.Unlock()
		// Accept round-end signals from before the connect returns, so an early
		// Disconnected is not lost.
		w.accepting.Store(true)
		if err := w.cli.ConnectContext(ctx); err != nil {
			w.accepting.Store(false)
			if ctx.Err() != nil {
				return
			}
			w.log.Error("connect for pairing", "err", err, "in", backoff)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		// A socket that died silently must not block us: the round needs a first code in time.
		firstTimer := time.NewTimer(firstQRTimeout)
		firstC := firstTimer.C
		ended, retry := false, false
		for !ended {
			select {
			case <-ctx.Done():
				firstTimer.Stop()
				w.endRound()
				return
			case <-w.paired:
				firstTimer.Stop()
				w.stopRotation()
				return
			case <-w.terminalCh:
				firstTimer.Stop()
				w.stopRotation()
				return
			case <-w.firstShown:
				firstTimer.Stop()
				firstC = nil
			case <-firstC:
				w.log.Warn("no QR code in time; restarting round")
				retry, ended = true, true
			case <-w.restart:
				ended = true
			case <-w.roundEnded:
				retry, ended = true, true
			}
		}
		firstTimer.Stop()
		w.endRound()
		if retry {
			w.mu.Lock()
			if w.delivered {
				backoff = time.Second
			}
			w.mu.Unlock()
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}
}

const firstQRTimeout = 30 * time.Second

// endRound stops the rotation, clears the now-expired code and drops the socket
// (never called from the event handler).
func (w *Whatsmeow) endRound() {
	w.accepting.Store(false)
	w.stopRotation()
	w.mu.Lock()
	w.ready, w.roundStart = nil, time.Time{}
	w.mu.Unlock()
	w.sink.QR("", 0)
	w.cli.Disconnect()
}

// stopRotation cancels the running code rotation, if any, and waits for it to exit.
func (w *Whatsmeow) stopRotation() {
	w.mu.Lock()
	cancel, done := w.rotCancel, w.rotDone
	w.rotCancel, w.rotDone = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// startRotation begins a round for an events.QR; called from handle(). It only waits
// for the previous rotation goroutine, which never blocks on the handler.
func (w *Whatsmeow) startRotation(codes []string) {
	w.stopRotation()
	w.mu.Lock()
	if !w.pairing || w.loopCtx.Err() != nil || w.terminal.Load() {
		w.mu.Unlock()
		return
	}
	rctx, cancel := context.WithCancel(w.loopCtx)
	ready, done := make(chan struct{}), make(chan struct{})
	w.rotCancel, w.rotDone = cancel, done
	w.ready = ready
	w.roundStart = time.Now()
	w.mu.Unlock()
	go func() {
		defer close(done)
		if rctx.Err() != nil || w.terminal.Load() {
			return
		}
		w.sink.ConnState("qr_pending", "", "")
		first := func() {
			w.mu.Lock()
			w.delivered = true
			w.mu.Unlock()
			close(ready)
			notify(w.firstShown)
		}
		if rotateQRs(rctx, slices.Clone(codes), defaultQRTiming, w.sink, w.rotate, first) {
			notify(w.roundEnded)
		}
	}()
}

type qrTiming struct{ First, Next time.Duration }

// defaultQRTiming mirrors whatsmeow's QR channel: first code 60s, the following ones 20s.
var defaultQRTiming = qrTiming{First: 60 * time.Second, Next: 20 * time.Second}

// rotateQRs shows each code for its timeout. first is called right after the first code is
// delivered. A RotateADVSecret rewrites the current and remaining codes. It returns true
// when every code expired, false when ctx ended first.
func rotateQRs(ctx context.Context, codes []string, t qrTiming, sink Sink,
	rot <-chan *events.RotateADVSecret, first func()) bool {
	timeout := t.First
	firstShown := false
	for len(codes) > 0 {
		sink.QR(codes[0], timeout)
		if !firstShown {
			firstShown = true
			first()
		}
		timer := time.NewTimer(timeout)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			codes = codes[1:]
		case r := <-rot:
			timer.Stop()
			for i := range codes {
				codes[i] = strings.Replace(codes[i], r.OldSecret, r.NewSecret, 1)
			}
		}
		timeout = t.Next
	}
	return true
}

func (w *Whatsmeow) reportTerminal(state, reason, jid string) {
	w.terminal.Store(true)
	notify(w.terminalCh)
	w.sink.ConnState(state, reason, jid)
}

func (w *Whatsmeow) handle(evt any) {
	loggedIn := w.cli.Store.ID != nil
	switch e := evt.(type) {
	case *events.QR:
		w.startRotation(e.Codes)
	case *events.RotateADVSecret:
		select {
		case w.rotate <- e:
		default:
		}
	case *events.PairSuccess:
		w.mu.Lock()
		if w.rotCancel != nil {
			w.rotCancel() // stop showing codes; pairLoop waits for the goroutine
		}
		w.mu.Unlock()
		notify(w.paired)
		w.sink.ConnState("connecting", "", e.ID.String())
	case *events.Connected, *events.KeepAliveRestored:
		w.sink.ConnState("connected", "", w.jid())
	case *events.Disconnected:
		if loggedIn {
			w.sink.ConnState("reconnecting", "disconnected", w.jid())
		} else if w.accepting.Load() {
			notify(w.roundEnded)
		}
	case *events.ConnectFailure:
		if !loggedIn {
			if w.accepting.Load() {
				notify(w.roundEnded)
			}
			return
		}
		// whatsmeow only emits ConnectFailure for reasons it does not handle itself, and
		// for those it disables auto-reconnect (expectDisconnect). Logouts and bans arrive
		// as LoggedOut/TemporaryBan instead; anything else we must reconnect ourselves,
		// or the worker would sit disconnected while its heartbeat still passes.
		if e.Reason.IsLoggedOut() || e.Reason == events.ConnectFailureTempBanned || w.terminal.Load() {
			return
		}
		w.sink.ConnState("reconnecting", "connect_failure_"+e.Reason.NumberString(), w.jid())
		w.mu.Lock()
		ctx := w.loopCtx
		w.mu.Unlock()
		if ctx != nil && ctx.Err() == nil {
			w.startConnectLoop(ctx, true)
		}
	case *events.KeepAliveTimeout:
		if loggedIn {
			w.sink.ConnState("reconnecting", "keepalive_timeout", w.jid())
		}
	case *events.LoggedOut:
		w.reportTerminal("logged_out", e.Reason.String(), "")
	case *events.StreamReplaced:
		// Another client took over this session; stop instead of fighting for it.
		w.reportTerminal("logged_out", "stream_replaced", w.jid())
	case *events.TemporaryBan:
		// A temporary ban expires and keeps the session: report a recoverable failure
		// (start works again later), never "banned", which the supervisor treats as permanent.
		w.reportTerminal("failed", fmt.Sprintf("temp_banned:%s", e.Expire), w.jid())
	case *events.ClientOutdated:
		w.reportTerminal("failed", "client_outdated", w.jid())
	}
}

func (w *Whatsmeow) jid() string {
	if w.cli.Store.ID == nil {
		return ""
	}
	return w.cli.Store.ID.String()
}

// PairPhone requests a pairing code. Codes die with the QR round (~160s), so when the
// current round is old a fresh one is started first and we wait for its first code.
func (w *Whatsmeow) PairPhone(ctx context.Context, phone string) (string, error) {
	w.mu.Lock()
	active, start, ready := w.pairing, w.roundStart, w.ready
	w.mu.Unlock()
	if !active {
		return "", errors.New("not pairing")
	}
	var stale chan struct{}
	if ready != nil && !start.IsZero() && time.Since(start) > 30*time.Second {
		notify(w.restart)
		stale = ready
	}
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := w.waitReady(wctx, stale); err != nil {
		return "", fmt.Errorf("waiting for pairing round: %w", err)
	}
	return w.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
}

// waitReady waits until a round other than stale has delivered its first QR code.
func (w *Whatsmeow) waitReady(ctx context.Context, stale chan struct{}) error {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		w.mu.Lock()
		ready, active := w.ready, w.pairing
		w.mu.Unlock()
		if !active {
			return errors.New("not pairing")
		}
		if ready != nil && ready != stale {
			select {
			case <-ready:
				return nil
			default:
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Logout unlinks the device. whatsmeow emits no event for a self-initiated logout,
// so we report logged_out ourselves. While still pairing there is nothing to unlink
// (cli.Logout would fail with ErrNotLoggedIn): stop the pairing loop, wait for it to be
// gone (so no late qr_pending), then report.
func (w *Whatsmeow) Logout(ctx context.Context) error {
	if w.cli.Store.ID == nil {
		w.terminal.Store(true)
		w.stopLoops()
		w.mu.Lock()
		done := w.loopDone
		w.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				w.log.Warn("pairing loop did not stop in time")
			}
		}
		w.cli.Disconnect()
		if w.cli.Store.ID != nil {
			w.log.Warn("device paired while logging out")
		}
		w.sink.ConnState("logged_out", "api", "")
		return nil
	}
	if err := w.cli.Logout(ctx); err != nil {
		return err
	}
	w.stopLoops()
	w.sink.ConnState("logged_out", "api", "")
	return nil
}

func (w *Whatsmeow) Disconnect() { w.cli.Disconnect() }
