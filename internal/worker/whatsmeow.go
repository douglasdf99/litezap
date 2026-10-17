package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Whatsmeow adapts a whatsmeow client to WAClient.
type Whatsmeow struct {
	cli  *whatsmeow.Client
	log  *slog.Logger
	sink Sink

	restart chan struct{} // asks qrLoop to end the current QR round immediately

	mu         sync.Mutex
	cancel     context.CancelFunc
	pairing    bool          // a QR round loop is active
	roundStart time.Time     // start of the current QR round
	ready      chan struct{} // closed when the current round delivered its first QR code
}

func NewWhatsmeow(cli *whatsmeow.Client, log *slog.Logger) *Whatsmeow {
	return &Whatsmeow{cli: cli, log: log, restart: make(chan struct{}, 1)}
}

func (w *Whatsmeow) Run(ctx context.Context, sink Sink) error {
	w.sink = sink
	w.cli.AddEventHandler(w.handle)
	loopCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
	if w.cli.Store.ID != nil {
		sink.ConnState("connecting", "", w.jid())
		go w.connectLoop(loopCtx)
		return nil
	}
	w.mu.Lock()
	w.pairing = true
	w.mu.Unlock()
	go w.qrLoop(loopCtx)
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

// connectLoop retries the initial connection (e.g. no network at boot) instead of
// exiting. With whatsmeow's InitialAutoReconnect enabled, Connect returns nil and
// retries internally, so this loop simply ends. Without it, we retry here instead of
// exiting, which would trip the supervisor's circuit breaker. Once connected,
// whatsmeow's own auto-reconnect takes over.
func (w *Whatsmeow) connectLoop(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := w.cli.ConnectContext(ctx)
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
		if delay < time.Minute {
			delay *= 2
		}
	}
}

func (w *Whatsmeow) beginRound() chan struct{} {
	ready := make(chan struct{})
	w.mu.Lock()
	w.roundStart = time.Now()
	w.ready = ready
	w.mu.Unlock()
	return ready
}

// qrLoop keeps offering QR codes until pairing succeeds or ctx ends. Rounds that end
// without success (all codes expired) restart after a backoff that resets whenever a
// code is delivered.
func (w *Whatsmeow) qrLoop(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.pairing = false
		w.mu.Unlock()
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		ch, err := w.cli.GetQRChannel(ctx)
		if err != nil {
			w.log.Error("get qr channel", "err", err)
			return
		}
		ready := w.beginRound()
		if err := w.cli.ConnectContext(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.log.Error("connect for pairing", "err", err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		announced, readyClosed, restarted := false, false, false
		var last whatsmeow.QRChannelItem
	round:
		for {
			select {
			case <-ctx.Done():
				w.cli.Disconnect()
				return
			case <-w.restart:
				restarted = true
				break round
			case item, ok := <-ch:
				if !ok {
					break round
				}
				last = item
				switch item.Event {
				case whatsmeow.QRChannelEventCode:
					if ctx.Err() != nil {
						continue
					}
					// Only report qr_pending once the socket is ready: PairPhone must wait for the first QR.
					if !announced {
						w.sink.ConnState("qr_pending", "", "")
						announced = true
					}
					w.sink.QR(item.Code, item.Timeout)
					backoff = time.Second
					if !readyClosed {
						close(ready)
						readyClosed = true
					}
				case whatsmeow.QRChannelSuccess.Event,
					whatsmeow.QRChannelClientOutdated.Event,
					whatsmeow.QRChannelErrUnexpectedEvent.Event:
					// handle() reports failed/banned for the error cases.
					return
				case whatsmeow.QRChannelEventError:
					w.log.Warn("pairing error", "err", item.Error)
				}
			}
		}
		// Round ended without success (codes exhausted or restart requested).
		w.log.Debug("qr round ended", "last", last.Event, "restarted", restarted)
		w.cli.Disconnect()
		if restarted {
			continue
		}
		if !sleep(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d *= 2; d > time.Minute {
		return time.Minute
	}
	return d
}

func (w *Whatsmeow) handle(evt any) {
	loggedIn := w.cli.Store.ID != nil
	switch e := evt.(type) {
	case *events.PairSuccess:
		w.sink.ConnState("connecting", "", e.ID.String())
	case *events.Connected, *events.KeepAliveRestored:
		w.sink.ConnState("connected", "", w.jid())
	case *events.Disconnected:
		if loggedIn {
			w.sink.ConnState("reconnecting", "disconnected", w.jid())
		}
	case *events.KeepAliveTimeout:
		if loggedIn {
			w.sink.ConnState("reconnecting", "keepalive_timeout", w.jid())
		}
	case *events.LoggedOut:
		w.sink.ConnState("logged_out", e.Reason.String(), "")
	case *events.StreamReplaced:
		// Another client took over this session; stop instead of fighting for it.
		w.sink.ConnState("logged_out", "stream_replaced", w.jid())
	case *events.TemporaryBan:
		w.sink.ConnState("banned", e.String(), w.jid())
	case *events.ClientOutdated:
		w.sink.ConnState("failed", "client_outdated", w.jid())
	}
}

func (w *Whatsmeow) jid() string {
	if w.cli.Store.ID == nil {
		return ""
	}
	return w.cli.Store.ID.String()
}

// PairPhone requests a pairing code. Codes die with the QR round (~160s), so when the
// current round is old a fresh one is started first and we wait for its first QR.
func (w *Whatsmeow) PairPhone(ctx context.Context, phone string) (string, error) {
	w.mu.Lock()
	active, start, old := w.pairing, w.roundStart, w.ready
	w.mu.Unlock()
	if !active {
		return "", errors.New("not pairing")
	}
	if time.Since(start) > 30*time.Second {
		select {
		case w.restart <- struct{}{}:
		default:
		}
	} else {
		old = nil
	}
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := w.waitReady(wctx, old); err != nil {
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
// (cli.Logout would fail with ErrNotLoggedIn): just stop the loops and disconnect.
func (w *Whatsmeow) Logout(ctx context.Context) error {
	if w.cli.Store.ID == nil {
		w.stopLoops()
		w.cli.Disconnect()
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
