package worker

import (
	"context"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Whatsmeow adapts a whatsmeow client to WAClient.
type Whatsmeow struct {
	cli  *whatsmeow.Client
	log  *slog.Logger
	sink Sink
}

func NewWhatsmeow(cli *whatsmeow.Client, log *slog.Logger) *Whatsmeow {
	return &Whatsmeow{cli: cli, log: log}
}

func (w *Whatsmeow) Run(ctx context.Context, sink Sink) error {
	w.sink = sink
	w.cli.AddEventHandler(w.handle)
	if w.cli.Store.ID != nil {
		sink.ConnState("connecting", "", w.jid())
		go w.connectLoop(ctx)
		return nil
	}
	go w.qrLoop(ctx)
	return nil
}

// connectLoop retries the initial connection (e.g. no network at boot) instead of
// exiting. With whatsmeow's InitialAutoReconnect enabled, Connect returns nil and
// retries internally, so this loop simply ends. Without it, we retry here instead of
// exiting, which would trip the supervisor's circuit breaker. Once connected,
// whatsmeow's own auto-reconnect takes over.
func (w *Whatsmeow) connectLoop(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := w.cli.Connect()
		if err == nil {
			return
		}
		w.log.Warn("connect failed; retrying", "err", err, "in", delay)
		w.sink.ConnState("reconnecting", "connect_failed", w.jid())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

// qrLoop keeps offering QR codes until pairing succeeds or ctx ends.
func (w *Whatsmeow) qrLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := w.cli.GetQRChannel(ctx)
		if err != nil {
			w.log.Error("get qr channel", "err", err)
			return
		}
		if err := w.cli.Connect(); err != nil {
			w.log.Error("connect for pairing", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		announced := false
		for item := range ch {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				// Only report qr_pending once the socket is ready: PairPhone must wait for the first QR.
				if !announced {
					w.sink.ConnState("qr_pending", "", "")
					announced = true
				}
				w.sink.QR(item.Code, item.Timeout)
			case whatsmeow.QRChannelSuccess.Event:
				return
			case whatsmeow.QRChannelEventError:
				w.log.Warn("pairing error", "err", item.Error)
			}
		}
		// Channel closed without success (codes exhausted): start a new round.
		w.cli.Disconnect()
	}
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

func (w *Whatsmeow) PairPhone(ctx context.Context, phone string) (string, error) {
	return w.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
}

// Logout unlinks the device. whatsmeow emits no event for a self-initiated logout,
// so we report logged_out ourselves.
func (w *Whatsmeow) Logout(ctx context.Context) error {
	if err := w.cli.Logout(ctx); err != nil {
		return err
	}
	w.sink.ConnState("logged_out", "api", "")
	return nil
}

func (w *Whatsmeow) Disconnect() { w.cli.Disconnect() }
