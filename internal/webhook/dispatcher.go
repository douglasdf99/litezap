package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

type Options struct {
	Clock clock.Clock
	// AllowPrivate lets webhooks reach private and loopback addresses (local setups).
	AllowPrivate bool
	// Client overrides the HTTP client (tests); by default an SSRF-safe client that never
	// follows redirects.
	Client      *http.Client
	Timeout     time.Duration // per attempt (default 10s)
	Concurrency int           // deliveries in flight overall (default 8)
	PerWebhook  int           // deliveries in flight per webhook (default 2)
	BackoffBase time.Duration // default 1s
	BackoffMax  time.Duration // default 1h
	MaxAge      time.Duration // give up after (default 24h)
	BufferSize  int           // events waiting to be written to the outbox (default 10000)
	// Retention keeps delivered deliveries (default 7 days); FailedRetention keeps the
	// dead-letter (default 30 days).
	Retention       time.Duration
	FailedRetention time.Duration
	SweepInterval   time.Duration // expiry and retention sweep (default 1 minute)
	Log             *slog.Logger
}

func (o *Options) defaults() {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Concurrency == 0 {
		o.Concurrency = 8
	}
	if o.PerWebhook == 0 {
		o.PerWebhook = 2
	}
	if o.BackoffBase == 0 {
		o.BackoffBase = time.Second
	}
	if o.BackoffMax == 0 {
		o.BackoffMax = time.Hour
	}
	if o.MaxAge == 0 {
		o.MaxAge = 24 * time.Hour
	}
	if o.BufferSize == 0 {
		o.BufferSize = 10000
	}
	if o.Retention == 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	if o.FailedRetention == 0 {
		o.FailedRetention = 30 * 24 * time.Hour
	}
	if o.SweepInterval == 0 {
		o.SweepInterval = time.Minute
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Client == nil {
		o.Client = &http.Client{
			Transport: media.SafeTransport(o.AllowPrivate),
			// A redirect is an answer, not a delivery: never follow it (it could point
			// anywhere, and the signature was made for this URL's owner).
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
}

// Dispatcher is the events.Sink of webhooks: Enqueue buffers events, a writer stores the
// matching deliveries in the outbox, and a scheduler sends them. Delivery is
// at-least-once: an attempt is recorded only after it ends, so a crash mid-attempt
// resends it after the restart (receivers deduplicate by X-LiteZap-Event-Id).
type Dispatcher struct {
	st  *store.Store
	opt Options
	log *slog.Logger

	in      chan events.Envelope
	inMu    sync.RWMutex // guards closing in
	inShut  bool
	dropped atomic.Int64

	hooksMu sync.RWMutex
	hooks   map[string]store.Webhook
	loadMu  sync.Mutex  // serializes LoadWebhooks
	stale   atomic.Bool // the last LoadWebhooks failed

	wake chan struct{}

	mu       sync.Mutex
	inflight map[string]int       // webhook id -> deliveries in flight
	busy     map[string]bool      // delivery ids in flight
	endpoint map[string]*endpoint // webhook id -> backoff state
	started  bool

	loopCancel context.CancelFunc
	sendCtx    context.Context
	sendCancel context.CancelFunc
	writerDone chan struct{}
	loopDone   chan struct{}
	sends      sync.WaitGroup
}

type endpoint struct {
	fails int
	until time.Time
}

func New(st *store.Store, opt Options) *Dispatcher {
	opt.defaults()
	sendCtx, sendCancel := context.WithCancel(context.Background())
	return &Dispatcher{st: st, opt: opt, log: opt.Log, in: make(chan events.Envelope, opt.BufferSize),
		hooks: map[string]store.Webhook{}, wake: make(chan struct{}, 1), inflight: map[string]int{},
		busy: map[string]bool{}, endpoint: map[string]*endpoint{}, sendCtx: sendCtx, sendCancel: sendCancel,
		writerDone: make(chan struct{}), loopDone: make(chan struct{})}
}

// Enqueue buffers an event; it never blocks. A full buffer drops the event (logged).
func (d *Dispatcher) Enqueue(e events.Envelope) {
	d.inMu.RLock()
	defer d.inMu.RUnlock()
	if d.inShut {
		d.dropped.Add(1)
		d.log.Warn("webhook: event after shutdown dropped", "event_id", e.ID, "type", e.Type)
		return
	}
	select {
	case d.in <- e:
	default:
		d.dropped.Add(1)
		d.log.Error("webhook: outbox buffer full; event dropped", "event_id", e.ID, "type", e.Type, "instance_id", e.InstanceID)
	}
}

// Dropped is the number of events not written to the outbox (full buffer, after Close).
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// LoadWebhooks refreshes the in-memory webhook list used to match events. Call it after
// every change to the webhooks table.
// A failed load marks the list stale: the writer and the scheduler retry it before using it.
func (d *Dispatcher) LoadWebhooks(ctx context.Context) error {
	// Serialized from list to swap: two concurrent reloads must not leave the older
	// snapshot in place (a webhook created meanwhile would never match).
	d.loadMu.Lock()
	defer d.loadMu.Unlock()
	list, err := d.st.ListWebhooks(ctx)
	if err != nil {
		d.stale.Store(true)
		return err
	}
	m := make(map[string]store.Webhook, len(list))
	for _, w := range list {
		m[w.ID] = w
	}
	d.hooksMu.Lock()
	d.hooks = m
	d.hooksMu.Unlock()
	d.stale.Store(false)
	d.mu.Lock()
	for id := range d.endpoint {
		if _, ok := m[id]; !ok {
			delete(d.endpoint, id) // deleted webhook
		}
	}
	d.mu.Unlock()
	return nil
}

// Reload is LoadWebhooks plus a scheduler wake-up (an enabled webhook may have work).
// On failure the list stays stale and is retried by the writer and the scheduler.
func (d *Dispatcher) Reload(ctx context.Context) error {
	err := d.LoadWebhooks(ctx)
	if err != nil {
		d.log.Error("webhook: reload webhooks; will retry", "err", err)
	}
	d.Wake()
	return err
}

// refreshIfStale retries a failed LoadWebhooks.
func (d *Dispatcher) refreshIfStale() {
	if d.stale.Load() {
		if err := d.LoadWebhooks(context.Background()); err != nil {
			d.log.Error("webhook: reload webhooks", "err", err)
		}
	}
}

func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Retry puts a failed delivery back in the queue with a fresh retry period.
// store.ErrNotFound means no failed delivery with that id belongs to the webhook.
func (d *Dispatcher) Retry(ctx context.Context, webhookID, deliveryID string) error {
	now := d.opt.Clock.Now()
	if err := d.st.RetryDelivery(ctx, webhookID, deliveryID, now, now.Add(d.opt.MaxAge)); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.endpoint, webhookID) // a manual retry probes the endpoint right away
	d.mu.Unlock()
	d.Wake()
	return nil
}

// Start loads the webhooks and starts the outbox writer and the scheduler.
func (d *Dispatcher) Start(ctx context.Context) error {
	if err := d.LoadWebhooks(ctx); err != nil {
		return err
	}
	lctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.started = true
	d.loopCancel = cancel
	d.mu.Unlock()
	go d.writer()
	go d.loop(lctx)
	return nil
}

// Close stops taking events, writes the buffered ones to the outbox, stops scheduling
// and waits for attempts in flight until ctx ends (then cancels them: they stay pending
// and are resent after the restart). Nothing touches the store after Close returns.
func (d *Dispatcher) Close(ctx context.Context) {
	d.inMu.Lock()
	if d.inShut {
		d.inMu.Unlock()
		return
	}
	d.inShut = true
	close(d.in)
	d.inMu.Unlock()

	d.mu.Lock()
	started := d.started
	d.mu.Unlock()
	if !started {
		d.drain() // never started (boot failed): still persist what was buffered
		return
	}
	<-d.writerDone // drains the buffer
	d.loopCancel()
	<-d.loopDone
	done := make(chan struct{})
	go func() { d.sends.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		d.sendCancel()
		<-done
	}
	d.sendCancel()
}

const writeBatch = 200

// writer moves buffered events into the outbox, in batches, until the buffer closes.
func (d *Dispatcher) writer() {
	defer close(d.writerDone)
	for e := range d.in {
		batch := []events.Envelope{e}
	more:
		for len(batch) < writeBatch {
			select {
			case e, ok := <-d.in:
				if !ok {
					break more
				}
				batch = append(batch, e)
			default:
				break more
			}
		}
		d.write(batch)
	}
}

// drain writes whatever is left in the (closed) buffer.
func (d *Dispatcher) drain() {
	var batch []events.Envelope
	for e := range d.in {
		batch = append(batch, e)
	}
	if len(batch) > 0 {
		d.write(batch)
	}
}

func (d *Dispatcher) write(batch []events.Envelope) {
	d.refreshIfStale()
	now := d.opt.Clock.Now()
	d.hooksMu.RLock()
	var ds []store.Delivery
	for _, e := range batch {
		for _, w := range d.hooks {
			if !matches(w, e) {
				continue
			}
			ds = append(ds, store.Delivery{ID: secure.NewID("dlv_"), WebhookID: w.ID, EventID: e.ID, EventType: e.Type,
				InstanceID: e.InstanceID, Payload: e.JSON, NextAttemptAt: now, ExpiresAt: now.Add(d.opt.MaxAge)})
		}
	}
	d.hooksMu.RUnlock()
	if len(ds) == 0 {
		return
	}
	var err error
	for try := 0; try < 3; try++ {
		if _, err = d.st.InsertDeliveries(context.Background(), ds, now); err == nil {
			d.Wake()
			return
		}
		time.Sleep(100 * time.Millisecond) // a rare store error: wait a moment, not a tight loop
	}
	d.dropped.Add(int64(len(batch)))
	d.log.Error("webhook: write outbox failed; events dropped", "events", len(batch), "err", err)
}

func matches(w store.Webhook, e events.Envelope) bool {
	return w.Enabled && w.Version == e.Version && (w.InstanceID == "" || w.InstanceID == e.InstanceID) &&
		events.MatchAny(w.Events, e.Type)
}

// loop schedules due deliveries until ctx ends.
func (d *Dispatcher) loop(ctx context.Context) {
	defer close(d.loopDone)
	nextSweep := d.opt.Clock.Now()
	for ctx.Err() == nil {
		now := d.opt.Clock.Now()
		if !now.Before(nextSweep) {
			d.sweep(ctx, now)
			nextSweep = now.Add(d.opt.SweepInterval)
		}
		d.refreshIfStale()
		wakeAt := d.schedule(ctx, now)
		if wakeAt.IsZero() || nextSweep.Before(wakeAt) {
			wakeAt = nextSweep
		}
		t := d.opt.Clock.NewTimer(wakeAt.Sub(now))
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-t.C():
		}
		t.Stop()
	}
}

func (d *Dispatcher) sweep(ctx context.Context, now time.Time) {
	if n, err := d.st.ExpireDeliveries(ctx, now); err != nil {
		d.log.Error("webhook: expire deliveries", "err", err)
	} else if n > 0 {
		d.log.Warn("webhook: deliveries moved to the dead-letter after the retry period", "count", n)
	}
	if _, err := d.st.PurgeDeliveries(ctx, store.DeliveryDelivered, now.Add(-d.opt.Retention)); err != nil {
		d.log.Error("webhook: purge delivered", "err", err)
	}
	if _, err := d.st.PurgeDeliveries(ctx, store.DeliveryFailed, now.Add(-d.opt.FailedRetention)); err != nil {
		d.log.Error("webhook: purge failed", "err", err)
	}
}

// schedule starts the due deliveries that fit and returns when to look again
// (zero: only on a wake-up).
func (d *Dispatcher) schedule(ctx context.Context, now time.Time) time.Time {
	d.hooksMu.RLock()
	var disabled []string
	for id, w := range d.hooks {
		if !w.Enabled {
			disabled = append(disabled, id)
		}
	}
	d.hooksMu.RUnlock()

	d.mu.Lock()
	exclude := append([]string(nil), disabled...)
	var wakeAt time.Time
	for id, ep := range d.endpoint {
		if ep.until.After(now) {
			exclude = append(exclude, id)
			if wakeAt.IsZero() || ep.until.Before(wakeAt) {
				wakeAt = ep.until
			}
		}
	}
	for id, n := range d.inflight {
		if n >= d.opt.PerWebhook {
			exclude = append(exclude, id) // a completion wakes us
		}
	}
	d.mu.Unlock()

	// A webhook that reaches its cap is excluded and the query repeated, so one busy
	// webhook's backlog cannot hold back the others' due deliveries.
	excluded := map[string]bool{}
	for round := 0; round < 32; round++ {
		d.mu.Lock()
		total := len(d.busy)
		d.mu.Unlock()
		free := d.opt.Concurrency - total
		if free <= 0 {
			break
		}
		limit := free + total // rows in flight are still pending: look past them
		due, err := d.st.DueDeliveries(ctx, now, exclude, limit)
		if err != nil {
			if ctx.Err() == nil {
				d.log.Error("webhook: load due deliveries", "err", err)
			}
			return now.Add(5 * time.Second)
		}
		capped := false
		for _, dl := range due {
			switch d.begin(dl) {
			case started:
				d.sends.Add(1)
				go d.attempt(dl)
			case webhookFull:
				if !excluded[dl.WebhookID] {
					excluded[dl.WebhookID] = true
					exclude = append(exclude, dl.WebhookID) // a completion wakes us
					capped = true
				}
			}
		}
		if !capped || len(due) < limit {
			break
		}
	}
	next, ok, err := d.st.NextDeliveryAt(ctx, exclude)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("webhook: next delivery", "err", err)
		}
		return now.Add(5 * time.Second)
	}
	if ok && next.After(now) && (wakeAt.IsZero() || next.Before(wakeAt)) {
		wakeAt = next
	}
	return wakeAt
}

type beginResult int

const (
	started     beginResult = iota
	skipped                 // already in flight, or no global slot left
	webhookFull             // the webhook has PerWebhook deliveries in flight
)

// begin reserves a slot for a delivery.
func (d *Dispatcher) begin(dl store.Delivery) beginResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.busy[dl.ID] || len(d.busy) >= d.opt.Concurrency:
		return skipped
	case d.inflight[dl.WebhookID] >= d.opt.PerWebhook:
		return webhookFull
	}
	d.busy[dl.ID] = true
	d.inflight[dl.WebhookID]++
	return started
}

func (d *Dispatcher) end(dl store.Delivery) {
	d.mu.Lock()
	delete(d.busy, dl.ID)
	if d.inflight[dl.WebhookID]--; d.inflight[dl.WebhookID] <= 0 {
		delete(d.inflight, dl.WebhookID)
	}
	d.mu.Unlock()
	d.Wake()
}

func (d *Dispatcher) attempt(dl store.Delivery) {
	defer d.sends.Done()
	defer d.end(dl)
	d.hooksMu.RLock()
	w, ok := d.hooks[dl.WebhookID]
	d.hooksMu.RUnlock()
	if !ok || !w.Enabled {
		return // deleted (its rows are gone) or disabled meanwhile: stays pending
	}
	code, err := d.post(w, dl)
	if d.sendCtx.Err() != nil {
		return // shutting down: not an attempt; resent after the restart
	}
	now := d.opt.Clock.Now()
	attempts := dl.Attempts + 1
	if err == nil && code/100 == 2 {
		d.mu.Lock()
		delete(d.endpoint, w.ID)
		d.mu.Unlock()
		if err := d.st.FinishDelivery(context.Background(), dl.ID, store.DeliveryDelivered, attempts, now, code, "", now); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			d.log.Error("webhook: record delivery", "webhook_id", w.ID, "delivery_id", dl.ID, "err", err)
			d.hold(w.ID, now.Add(recordErrPause)) // still pending: do not resend in a tight loop
		}
		return
	}
	msg := "http " + strconv.Itoa(code)
	if err != nil {
		msg = err.Error()
	}
	d.mu.Lock()
	ep := d.endpoint[w.ID]
	if ep == nil {
		ep = &endpoint{}
		d.endpoint[w.ID] = ep
	}
	// Concurrent failures within one backoff period count once.
	if !ep.until.After(now) {
		ep.fails++
		ep.until = now.Add(Backoff(ep.fails, d.opt.BackoffBase, d.opt.BackoffMax))
	}
	d.mu.Unlock()

	status, next := store.DeliveryPending, now.Add(Backoff(attempts, d.opt.BackoffBase, d.opt.BackoffMax))
	if next.After(dl.ExpiresAt) {
		status, next = store.DeliveryFailed, now
		d.log.Warn("webhook: delivery moved to the dead-letter", "webhook_id", w.ID, "delivery_id", dl.ID,
			"event_type", dl.EventType, "attempts", attempts, "last_error", msg)
	}
	if err := d.st.FinishDelivery(context.Background(), dl.ID, status, attempts, next, code, msg, now); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		d.log.Error("webhook: record attempt", "webhook_id", w.ID, "delivery_id", dl.ID, "err", err)
		d.hold(w.ID, now.Add(recordErrPause))
	}
}

const recordErrPause = 5 * time.Second

// hold keeps a webhook's deliveries back until at least until.
func (d *Dispatcher) hold(webhookID string, until time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ep := d.endpoint[webhookID]
	if ep == nil {
		ep = &endpoint{}
		d.endpoint[webhookID] = ep
	}
	if until.After(ep.until) {
		ep.until = until
	}
}

// post sends one delivery and returns the HTTP status. Errors never include the URL
// (it may carry credentials) nor the secret.
func (d *Dispatcher) post(w store.Webhook, dl store.Delivery) (int, error) {
	ctx, cancel := context.WithTimeout(d.sendCtx, d.opt.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(dl.Payload))
	if err != nil {
		return 0, errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LiteZap-Webhook/1")
	req.Header.Set(HeaderEventID, dl.EventID)
	req.Header.Set(HeaderEventType, dl.EventType)
	req.Header.Set(HeaderSignature, Sign(w.Secret, d.opt.Clock.Now(), dl.Payload))
	resp, err := d.opt.Client.Do(req)
	if err != nil {
		return 0, cleanErr(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// cleanErr drops the URL from a client error.
func cleanErr(err error) error {
	if errors.Is(err, media.ErrBlocked) {
		return errors.New("blocked: webhook URL points to a private or local address (see webhook_allow_private_urls)")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("timeout: no answer within the delivery timeout")
		}
		return fmt.Errorf("request failed: %v", ue.Err)
	}
	return err
}
