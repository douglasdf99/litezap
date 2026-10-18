package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestSignVerify(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	h := Sign("whsec_x", t0, body)
	if !strings.HasPrefix(h, "t=1792238400,v1=") {
		t.Fatalf("header = %s", h)
	}
	if err := Verify("whsec_x", h, body, t0.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := Verify("whsec_y", h, body, t0, 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := Verify("whsec_x", h, []byte(`{"id":"evt_2"}`), t0, 5*time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered body: %v", err)
	}
	if err := Verify("whsec_x", h, body, t0.Add(6*time.Minute), 5*time.Minute); !errors.Is(err, ErrStale) {
		t.Fatalf("replay: %v", err)
	}
	if err := Verify("whsec_x", "garbage", body, t0, time.Minute); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage: %v", err)
	}
	if a, b := NewSecret(), NewSecret(); a == b || !strings.HasPrefix(a, "whsec_") || len(a) < 40 {
		t.Fatalf("secrets %q %q", a, b)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := Backoff(i+1, time.Second, time.Hour); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := Backoff(13, time.Second, time.Hour); got != time.Hour {
		t.Errorf("Backoff(13) = %v", got)
	}
	if got := Backoff(500, time.Second, time.Hour); got != time.Hour {
		t.Errorf("Backoff(500) = %v", got)
	}
}

// receiver is a test endpoint; status decides each answer.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	status func(n int) int
}

func newReceiver(t *testing.T, status func(n int) int) *receiver {
	r := &receiver{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.bodies = append(r.bodies, b)
		n := len(r.reqs)
		r.mu.Unlock()
		w.WriteHeader(r.status(n))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reqs) }

type env struct {
	t  *testing.T
	st *store.Store
	fc *clock.Fake
	d  *Dispatcher
}

func newEnv(t *testing.T, tweak func(*Options)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range []string{"i1", "i2"} {
		if err := st.CreateInstance(context.Background(), store.Instance{ID: id, Name: id, State: instance.Created, TokenHash: id}); err != nil {
			t.Fatal(err)
		}
	}
	fc := clock.NewFake(t0)
	opt := Options{Clock: fc, AllowPrivate: true, Log: slog.New(slog.DiscardHandler)}
	if tweak != nil {
		tweak(&opt)
	}
	d := New(st, opt)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Close(ctx)
	})
	return &env{t: t, st: st, fc: fc, d: d}
}

func (e *env) hook(id, instanceID, url string, evs ...string) {
	e.t.Helper()
	if err := e.st.CreateWebhook(context.Background(), store.Webhook{ID: id, InstanceID: instanceID, URL: url,
		Secret: "secret-" + id, Events: evs, Version: 1, Enabled: true}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) start() {
	e.t.Helper()
	if err := e.d.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// publish sends an event through a bus whose sink is the dispatcher.
func (e *env) publish(instanceID, typ string) events.Envelope {
	var got events.Envelope
	b := events.NewBus(e.fc, sinkFunc(func(env events.Envelope) { got = env; e.d.Enqueue(env) }))
	b.Publish(instanceID, typ, map[string]string{"k": "v"})
	return got
}

type sinkFunc func(events.Envelope)

func (f sinkFunc) Enqueue(e events.Envelope) { f(e) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// advance moves the fake clock once the scheduler waits on its timer (it reads the clock
// only after stopping its previous timer, so the new timer sees the advanced time).
func (e *env) advance(d time.Duration) {
	e.t.Helper()
	eventually(e.t, "scheduler waiting", func() bool { return e.fc.Waiters() >= 1 })
	e.fc.Advance(d)
}

func (e *env) deliveries(webhookID, status string) []store.Delivery {
	ds, _, err := e.st.ListDeliveries(context.Background(), webhookID, status, 100, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return ds
}

func TestDeliversSignedMatchingEvents(t *testing.T) {
	e := newEnv(t, nil)
	r := newReceiver(t, func(int) int { return 204 })
	other := newReceiver(t, func(int) int { return 200 })
	e.hook("wh_all", "", r.srv.URL, "message.*")
	e.hook("wh_i2", "i2", other.srv.URL, "*")
	e.start()
	ev := e.publish("i1", events.TypeMessageReceived)
	e.publish("i1", events.TypeInstanceStatus) // not matched by message.*
	eventually(t, "delivered", func() bool { return len(e.deliveries("wh_all", store.DeliveryDelivered)) == 1 })
	if r.count() != 1 || other.count() != 0 {
		t.Fatalf("requests: matching=%d other-instance=%d", r.count(), other.count())
	}
	req, body := r.reqs[0], r.bodies[0]
	if string(body) != string(ev.JSON) {
		t.Fatalf("body = %s, want %s", body, ev.JSON)
	}
	if req.Header.Get(HeaderEventID) != ev.ID || req.Header.Get(HeaderEventType) != events.TypeMessageReceived ||
		req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", req.Header)
	}
	if err := Verify("secret-wh_all", req.Header.Get(HeaderSignature), body, t0, 5*time.Minute); err != nil {
		t.Fatalf("signature: %v", err)
	}
	d := e.deliveries("wh_all", "")[0]
	if d.Attempts != 1 || d.LastStatusCode != 204 || d.EventID != ev.ID {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestRetriesWithBackoffThenDelivers(t *testing.T) {
	e := newEnv(t, nil)
	r := newReceiver(t, func(n int) int {
		if n < 3 {
			return 500
		}
		return 200
	})
	e.hook("wh", "", r.srv.URL, "*")
	e.start()
	e.publish("i1", events.TypeMessageReceived)
	eventually(t, "first attempt", func() bool { return r.count() == 1 })
	eventually(t, "attempt recorded", func() bool { d := e.deliveries("wh", ""); return len(d) == 1 && d[0].Attempts == 1 })
	d := e.deliveries("wh", "")[0]
	if d.Status != store.DeliveryPending || d.LastStatusCode != 500 || !d.NextAttemptAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("after 1st failure: %+v", d)
	}
	e.advance(999 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if r.count() != 1 {
		t.Fatal("retried before the backoff")
	}
	e.advance(time.Millisecond)
	eventually(t, "second attempt", func() bool { return r.count() == 2 })
	eventually(t, "2nd attempt recorded", func() bool { return e.deliveries("wh", "")[0].Attempts == 2 })
	if d := e.deliveries("wh", "")[0]; !d.NextAttemptAt.Equal(t0.Add(3 * time.Second)) {
		t.Fatalf("2nd backoff: next = %v", d.NextAttemptAt)
	}
	e.advance(2 * time.Second)
	eventually(t, "delivered", func() bool { return len(e.deliveries("wh", store.DeliveryDelivered)) == 1 })
	if d := e.deliveries("wh", "")[0]; d.Attempts != 3 || d.LastError != "" {
		t.Fatalf("delivered = %+v", d)
	}
}

// A failing endpoint holds its other deliveries back (no hammering) while other
// endpoints keep going.
func TestEndpointBackoffHoldsOtherDeliveries(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.PerWebhook = 1 })
	var down atomic.Bool
	down.Store(true)
	bad := newReceiver(t, func(int) int {
		if down.Load() {
			return 503
		}
		return 200
	})
	good := newReceiver(t, func(int) int { return 200 })
	e.hook("wh_bad", "", bad.srv.URL, "*")
	e.hook("wh_good", "", good.srv.URL, "*")
	e.start()
	for i := 0; i < 3; i++ {
		e.publish("i1", events.TypeMessageReceived)
	}
	eventually(t, "good got all", func() bool { return len(e.deliveries("wh_good", store.DeliveryDelivered)) == 3 })
	eventually(t, "bad tried once", func() bool { return bad.count() == 1 })
	time.Sleep(30 * time.Millisecond)
	if bad.count() != 1 {
		t.Fatalf("failing endpoint got %d requests before its backoff ended", bad.count())
	}
	down.Store(false)
	for i := 0; i < 50 && len(e.deliveries("wh_bad", store.DeliveryDelivered)) < 3; i++ {
		e.fc.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	eventually(t, "bad recovered", func() bool { return len(e.deliveries("wh_bad", store.DeliveryDelivered)) == 3 })
}

func TestGivesUpIntoDeadLetterAndManualRetry(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxAge = 10 * time.Second })
	var ok atomic.Bool
	r := newReceiver(t, func(int) int {
		if ok.Load() {
			return 200
		}
		return 500
	})
	e.hook("wh", "", r.srv.URL, "*")
	e.start()
	e.publish("i1", events.TypeMessageReceived)
	// Attempts at 0s, 1s, 3s, 7s; the next (15s) is past the 10s limit: dead-letter.
	for i := 0; i < 40 && len(e.deliveries("wh", store.DeliveryFailed)) == 0; i++ {
		e.fc.Advance(500 * time.Millisecond)
		time.Sleep(5 * time.Millisecond)
	}
	failed := e.deliveries("wh", store.DeliveryFailed)
	if len(failed) != 1 || failed[0].Attempts != 4 || failed[0].LastStatusCode != 500 || r.count() != 4 {
		t.Fatalf("failed = %+v, requests = %d", failed, r.count())
	}
	ok.Store(true)
	if err := e.d.Retry(context.Background(), "wh", failed[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "redelivered", func() bool { return len(e.deliveries("wh", store.DeliveryDelivered)) == 1 })
	if err := e.d.Retry(context.Background(), "wh", failed[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retry of a delivered delivery: %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	e := newEnv(t, nil)
	target := newReceiver(t, func(int) int { return 200 })
	redir := httptest.NewServer(http.RedirectHandler(target.srv.URL, http.StatusFound))
	defer redir.Close()
	e.hook("wh", "", redir.URL, "*")
	e.start()
	e.publish("i1", events.TypeMessageReceived)
	eventually(t, "attempt", func() bool { d := e.deliveries("wh", ""); return len(d) == 1 && d[0].Attempts == 1 })
	if d := e.deliveries("wh", "")[0]; d.LastStatusCode != 302 || d.Status != store.DeliveryPending || target.count() != 0 {
		t.Fatalf("delivery = %+v, target hits = %d", d, target.count())
	}
}

func TestPrivateAddressBlockedByDefault(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.AllowPrivate = false })
	r := newReceiver(t, func(int) int { return 200 })
	e.hook("wh", "", r.srv.URL, "*")
	e.start()
	e.publish("i1", events.TypeMessageReceived)
	eventually(t, "attempt", func() bool { d := e.deliveries("wh", ""); return len(d) == 1 && d[0].Attempts == 1 })
	d := e.deliveries("wh", "")[0]
	if r.count() != 0 || !strings.Contains(d.LastError, "private") || strings.Contains(d.LastError, r.srv.URL) {
		t.Fatalf("delivery = %+v, hits = %d", d, r.count())
	}
}

func TestDisabledAndReloaded(t *testing.T) {
	e := newEnv(t, nil)
	r := newReceiver(t, func(int) int { return 200 })
	e.start()
	e.publish("i1", events.TypeMessageReceived) // no webhook yet
	e.hook("wh", "", r.srv.URL, "*")
	if err := e.d.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.publish("i1", events.TypeMessageReceived)
	eventually(t, "delivered", func() bool { return len(e.deliveries("wh", store.DeliveryDelivered)) == 1 })
	w, _ := e.st.GetWebhook(context.Background(), "wh")
	w.Enabled = false
	if err := e.st.UpdateWebhook(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.publish("i1", events.TypeMessageReceived)
	time.Sleep(30 * time.Millisecond)
	if n := len(e.deliveries("wh", "")); n != 1 || r.count() != 1 {
		t.Fatalf("disabled webhook: %d deliveries, %d requests", n, r.count())
	}
}

// Close writes buffered events to the outbox: a restart delivers them.
func TestCloseDrainsBufferToOutbox(t *testing.T) {
	e := newEnv(t, nil)
	r := newReceiver(t, func(int) int { return 200 })
	e.hook("wh", "", r.srv.URL, "*")
	// Not started: events wait in the buffer (as during boot), Start/Close must persist them.
	e.publish("i1", events.TypeMessageReceived)
	e.publish("i1", events.TypeMessageSent)
	if err := e.d.LoadWebhooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.d.Close(ctx)
	if n := len(e.deliveries("wh", store.DeliveryPending)); n != 2 {
		t.Fatalf("pending after close = %d", n)
	}
	e.d.Enqueue(events.Envelope{ID: "evt_late"}) // after Close: dropped, no panic

	d2 := New(e.st, Options{Clock: e.fc, AllowPrivate: true, Log: slog.New(slog.DiscardHandler)})
	if err := d2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer d2.Close(context.Background())
	eventually(t, "delivered after restart", func() bool { return len(e.deliveries("wh", store.DeliveryDelivered)) == 2 })
}

func TestFullBufferDropsWithoutBlocking(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.BufferSize = 1 })
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			e.d.Enqueue(events.Envelope{ID: "evt", Type: events.TypeMessageReceived})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked")
	}
	if e.d.Dropped() != 4 {
		t.Fatalf("dropped = %d", e.d.Dropped())
	}
}

func TestRetentionAndExpirySweep(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Retention, o.FailedRetention, o.SweepInterval = time.Hour, 2*time.Hour, time.Minute
	})
	ctx := context.Background()
	e.hook("wh", "", "http://127.0.0.1:1/never", "*")
	if _, err := e.st.InsertDeliveries(ctx, []store.Delivery{
		{ID: "old", WebhookID: "wh", EventID: "e1", EventType: "x", Payload: []byte("{}"), NextAttemptAt: t0.Add(100 * time.Hour), ExpiresAt: t0.Add(time.Minute)},
		{ID: "done", WebhookID: "wh", EventID: "e2", EventType: "x", Payload: []byte("{}"), NextAttemptAt: t0.Add(100 * time.Hour), ExpiresAt: t0.Add(24 * time.Hour)},
	}, t0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishDelivery(ctx, "done", store.DeliveryDelivered, 1, t0, 200, "", t0); err != nil {
		t.Fatal(err)
	}
	e.start()
	for i := 0; i < 10 && len(e.deliveries("wh", store.DeliveryFailed)) == 0; i++ {
		e.fc.Advance(time.Minute)
		time.Sleep(5 * time.Millisecond)
	}
	if f := e.deliveries("wh", store.DeliveryFailed); len(f) != 1 || f[0].ID != "old" || !strings.HasPrefix(f[0].LastError, "expired") {
		t.Fatalf("expired = %+v", f)
	}
	for i := 0; i < 200 && len(e.deliveries("wh", "")) > 0; i++ {
		e.fc.Advance(time.Minute)
		time.Sleep(2 * time.Millisecond)
	}
	if n := len(e.deliveries("wh", "")); n != 0 {
		t.Fatalf("%d deliveries survived retention", n)
	}
}

// One webhook's backlog must not hold back another webhook's due delivery.
func TestBusyWebhookDoesNotStarveOthers(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.PerWebhook, o.Concurrency = 1, 2 })
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release) // runs first: Close waits for the blocked handler
	fast := newReceiver(t, func(int) int { return 200 })
	e.hook("wh_slow", "", slow.URL, "*")
	e.hook("wh_fast", "", fast.srv.URL, "*")
	var ds []store.Delivery
	for _, ev := range []string{"e1", "e2", "e3", "e4", "e5"} {
		ds = append(ds, store.Delivery{ID: "s" + ev, WebhookID: "wh_slow", EventID: ev, EventType: "x", Payload: []byte("{}"),
			NextAttemptAt: t0, ExpiresAt: t0.Add(time.Hour)})
	}
	ds = append(ds, store.Delivery{ID: "f1", WebhookID: "wh_fast", EventID: "e1", EventType: "x", Payload: []byte("{}"),
		NextAttemptAt: t0, ExpiresAt: t0.Add(time.Hour)})
	if _, err := e.st.InsertDeliveries(context.Background(), ds, t0); err != nil {
		t.Fatal(err)
	}
	e.start()
	eventually(t, "fast webhook delivered while the slow one is busy", func() bool {
		return len(e.deliveries("wh_fast", store.DeliveryDelivered)) == 1
	})
}

// Concurrent reloads end with the latest webhook list.
func TestConcurrentReloadsKeepLatestList(t *testing.T) {
	e := newEnv(t, nil)
	e.start()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.hook("wh_"+strconv.Itoa(i), "", "https://example.invalid/", "*")
			_ = e.d.Reload(context.Background())
		}()
	}
	wg.Wait()
	e.d.hooksMu.RLock()
	n := len(e.d.hooks)
	e.d.hooksMu.RUnlock()
	if n != 20 {
		t.Fatalf("dispatcher knows %d of 20 webhooks", n)
	}
}
