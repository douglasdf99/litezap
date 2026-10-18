// Package queue dispatches the persistent send queue (spec 4.1): one goroutine per
// instance, strict per-recipient order, pacing, the new-number ramp and retries.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Payload is what the service stores in outbound_queue.payload_json.
type Payload struct {
	// text | image | video | gif | ptv | audio | document | sticker | location | contact |
	// poll | reaction | edit | revoke | pin | forward
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// MediaFile is the staged attachment; it is deleted once the item is final.
	MediaFile string          `json:"media_file,omitempty"`
	Mime      string          `json:"mime,omitempty"`
	FileName  string          `json:"file_name,omitempty"`
	Size      int64           `json:"size,omitempty"`
	PTT       bool            `json:"ptt,omitempty"`
	Animated  bool            `json:"animated,omitempty"` // sticker
	Reaction  *ReactionTarget `json:"reaction,omitempty"`
	// Meta is the structured content (location, contacts, poll, reply, mentions...), sent
	// and logged with the message.
	Meta        *store.MessageMeta `json:"meta,omitempty"`
	Quote       *QuoteTarget       `json:"quote,omitempty"`
	MentionAll  bool               `json:"mention_all,omitempty"`
	LinkPreview *LinkPreview       `json:"link_preview,omitempty"`
	// Target is the message an edit, revoke or pin changes.
	Target     *Target `json:"target,omitempty"`
	Unpin      bool    `json:"unpin,omitempty"`
	PinSeconds uint32  `json:"pin_seconds,omitempty"`
	// ForwardKind/ForwardRef: a received attachment forwarded as is (no new upload).
	ForwardKind string `json:"forward_kind,omitempty"`
	ForwardRef  []byte `json:"forward_ref,omitempty"`
	// DelayMS waits before the send and TypingMS shows "typing..." for that long (both
	// bounded by the service). They only add time: the pacing floors still apply.
	DelayMS  int `json:"delay_ms,omitempty"`
	TypingMS int `json:"typing_ms,omitempty"`
	// StatusStyle is the look of a text status (marco 10).
	StatusStyle *StatusStyle `json:"status_style,omitempty"`
}

// StatusStyle is the background, text color (ARGB) and font of a text status.
type StatusStyle struct {
	BackgroundARGB uint32 `json:"background_argb,omitempty"`
	TextARGB       uint32 `json:"text_argb,omitempty"`
	Font           int32  `json:"font,omitempty"`
}

// QuoteTarget is the message a reply quotes.
type QuoteTarget struct {
	WAMessageID string `json:"wa_message_id"`
	FromMe      bool   `json:"from_me"`
	Sender      string `json:"sender,omitempty"`
	Text        string `json:"text,omitempty"`
	// RemoteJID: the quoted message is in another chat (a contact's status).
	RemoteJID string `json:"remote_jid,omitempty"`
}

// LinkPreview is the preview card of a text (thumbnail: small JPEG).
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Thumbnail   []byte `json:"thumbnail,omitempty"`
}

// Target is the logged message an edit, revoke or pin refers to.
type Target struct {
	MessageID   string    `json:"message_id"` // LiteZap id
	WAMessageID string    `json:"wa_message_id"`
	FromMe      bool      `json:"from_me"`
	Sender      string    `json:"sender,omitempty"`
	SentAt      time.Time `json:"sent_at"`
}

// Edit and revoke windows: WhatsApp documents 15 minutes for edits (whatsmeow.EditWindow
// says 20; later edits may be ignored by recipients) and about two days to delete for
// everyone. Checked again when the item is dispatched: a queued edit may have waited too long.
const (
	EditWindow   = 15 * time.Minute
	RevokeWindow = 48 * time.Hour
)

// Updates reports whether a type changes another message instead of sending a new one
// (not logged as a message of its own).
func Updates(typ string) bool { return typ == "edit" || typ == "revoke" || typ == "pin" }

// windowError is the last_error of an edit or revoke whose window closed (empty: still open).
func windowError(p Payload, now time.Time) string {
	if p.Target == nil || p.Target.SentAt.IsZero() {
		return ""
	}
	age := now.Sub(p.Target.SentAt)
	switch {
	case p.Type == "edit" && age > EditWindow:
		return "edit_window_expired: messages can only be edited for 15 minutes"
	case p.Type == "revoke" && age > RevokeWindow:
		return "revoke_window_expired: messages can only be deleted for everyone for 48 hours"
	}
	return ""
}

// ReactionTarget identifies the message a reaction refers to.
type ReactionTarget struct {
	WAMessageID string `json:"wa_message_id"`
	FromMe      bool   `json:"from_me"`
	Sender      string `json:"sender,omitempty"`
}

// Workers gives access to running workers (implemented by *manager.Manager).
type Workers interface {
	Client(instanceID string) (workerv1.WorkerClient, error)
}

type Options struct {
	Clock    clock.Clock
	Rand     func() float64 // jitter source, [0,1)
	Defaults settings.Settings
	// MaxAttempts bounds retryable failures (spec: 5).
	MaxAttempts int
	// SendTimeout bounds one Send RPC; whatsmeow itself waits up to 75s for the server ack.
	SendTimeout time.Duration
	// ConnectGrace delays the first send after reaching connected by ConnectGrace plus a
	// random part of up to ConnectGrace (0 disables; serve uses 15s).
	ConnectGrace time.Duration
	// WindowJitter spreads the first send of each send window over [0, WindowJitter)
	// (default 5 minutes), so queued work does not start exactly on the hour.
	WindowJitter time.Duration
	// Resolver, when set, resolves a phone number before the first send to it.
	Resolver *lookup.Resolver
	Log      *slog.Logger
	// Events, when set, receives message.sent and message.status (sent, failed) for API sends.
	Events events.Publisher
	// Hold, when set, pauses an instance's queue while it returns true (operator send
	// suspension); whoever releases it must call Wake.
	Hold func(instanceID string) bool
}

// Queues owns the per-instance dispatchers. It is a manager.Observer: reaching connected
// wakes the instance's dispatcher.
type Queues struct {
	store   *store.Store
	workers Workers
	opt     Options

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	disp    map[string]*dispatcher
	waiters map[string][]chan struct{} // by waiterKey(instance, item)
	readyAt map[string]time.Time       // end of the grace period after connecting
}

func New(st *store.Store, workers Workers, opt Options) *Queues {
	if opt.Clock == nil {
		opt.Clock = clock.Real{}
	}
	if opt.Rand == nil {
		opt.Rand = rand.Float64
	}
	if opt.MaxAttempts == 0 {
		opt.MaxAttempts = 5
	}
	if opt.SendTimeout == 0 {
		opt.SendTimeout = 90 * time.Second
	}
	if opt.WindowJitter == 0 {
		opt.WindowJitter = 5 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Queues{store: st, workers: workers, opt: opt, ctx: ctx, cancel: cancel,
		disp: map[string]*dispatcher{}, waiters: map[string][]chan struct{}{}, readyAt: map[string]time.Time{}}
}

// Recover runs at boot, before any worker starts: in_flight items go back to queued.
func (q *Queues) Recover(ctx context.Context) (int, error) { return q.store.RecoverInFlight(ctx) }

func (q *Queues) WorkerEvent(string, *workerv1.Event) {}

// notReady marks an instance whose connected notice has not arrived yet.
var notReady = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

func (q *Queues) StateChanged(id string, st instance.State, _ string) {
	g := q.opt.ConnectGrace
	if st != instance.Connected {
		if g > 0 {
			// The stored state may read connected before this notice arrives: hold sends
			// until the connected notice starts the grace period.
			q.mu.Lock()
			q.readyAt[id] = notReady
			q.mu.Unlock()
		}
		return
	}
	if g > 0 {
		ready := q.opt.Clock.Now().Add(g + time.Duration(q.opt.Rand()*float64(g)))
		q.mu.Lock()
		q.readyAt[id] = ready
		q.mu.Unlock()
	}
	q.Wake(id)
}

func (q *Queues) ready(id string) time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.readyAt[id]
}

// Wake makes the instance's dispatcher look at the queue now (new item, new settings,
// connected), starting it if needed.
func (q *Queues) Wake(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	d := q.disp[id]
	if d == nil {
		d = &dispatcher{q: q, id: id, wake: make(chan struct{}, 1), pacer: pacer{rand: q.opt.Rand}}
		q.disp[id] = d
		q.wg.Add(1)
		go d.run()
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Forget drops a deleted instance: its dispatcher wakes, finds the instance gone and
// exits; Wait calls on its items return (the items went with the instance).
func (q *Queues) Forget(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.readyAt, id)
	if d := q.disp[id]; d != nil {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	prefix := id + "/"
	for k, chans := range q.waiters {
		if strings.HasPrefix(k, prefix) {
			for _, ch := range chans {
				close(ch)
			}
			delete(q.waiters, k)
		}
	}
}

// Dispatchers is the number of running dispatchers (for tests and diagnostics).
func (q *Queues) Dispatchers() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.disp)
}

func waiterKey(instanceID, id string) string { return instanceID + "/" + id }

// Shutdown stops the dispatchers; a send in progress is allowed to finish until ctx ends.
func (q *Queues) Shutdown(ctx context.Context) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func final(st string) bool { return st == store.OutSent || st == store.OutFailed }

// Wait blocks until the item is sent or failed, or ctx ends, and returns its latest state
// (still queued or in_flight when ctx ended first).
func (q *Queues) Wait(ctx context.Context, instanceID, id string) (store.Outbound, error) {
	key := waiterKey(instanceID, id)
	for {
		ch := make(chan struct{})
		q.mu.Lock()
		q.waiters[key] = append(q.waiters[key], ch)
		q.mu.Unlock()
		o, err := q.store.GetOutbound(ctx, instanceID, id)
		if err != nil || final(o.Status) {
			q.dropWaiter(key, ch)
			return o, err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			q.dropWaiter(key, ch)
			// The request context is over, not the store: read with a fresh one.
			o, err = q.store.GetOutbound(context.WithoutCancel(ctx), instanceID, id)
			return o, err
		}
	}
}

func (q *Queues) dropWaiter(key string, ch chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.waiters[key]
	for i, c := range l {
		if c == ch {
			q.waiters[key] = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(q.waiters[key]) == 0 {
		delete(q.waiters, key)
	}
}

// notify wakes the Wait calls of an item whose status changed.
func (q *Queues) notify(instanceID, id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	key := waiterKey(instanceID, id)
	for _, ch := range q.waiters[key] {
		close(ch)
	}
	delete(q.waiters, key)
}

type dispatcher struct {
	q     *Queues
	id    string
	wake  chan struct{}
	pacer pacer
	// lastNewText/lastNewChat: the text and chat of the previous send when it went to a new
	// recipient.
	lastNewText, lastNewChat string
	// winStart/winJitter: the jitter drawn for the send window starting at winStart.
	winStart  time.Time
	winJitter time.Duration
}

// windowOpensAt is when sending may start in the window beginning at start (start + jitter).
func (d *dispatcher) windowOpensAt(start time.Time) time.Time {
	if !start.Equal(d.winStart) {
		d.winStart = start
		d.winJitter = time.Duration(d.q.opt.Rand() * float64(d.q.opt.WindowJitter))
	}
	return start.Add(d.winJitter)
}

func (d *dispatcher) log() *slog.Logger { return d.q.opt.Log.With("instance_id", d.id) }

func (d *dispatcher) run() {
	defer func() {
		d.q.mu.Lock()
		if d.q.disp[d.id] == d {
			delete(d.q.disp, d.id)
		}
		d.q.mu.Unlock()
		d.q.wg.Done()
	}()
	ctx := d.q.ctx
	for ctx.Err() == nil {
		next, alive := d.step(ctx)
		if !alive {
			return
		}
		d.sleep(ctx, next)
	}
}

// sleep waits for d, a wake-up or the end of ctx; d < 0 waits for a wake-up only.
func (d *dispatcher) sleep(ctx context.Context, dur time.Duration) {
	if dur == 0 {
		return
	}
	var timer <-chan time.Time
	if dur > 0 {
		t := d.q.opt.Clock.NewTimer(dur)
		defer t.Stop()
		timer = t.C()
	}
	select {
	case <-ctx.Done():
	case <-d.wake:
	case <-timer:
	}
}

const errPause = 5 * time.Second // pause after an unexpected store error

// step does at most one send and returns how long to sleep before the next step
// (-1: until woken). alive=false means the instance no longer exists.
func (d *dispatcher) step(ctx context.Context) (next time.Duration, alive bool) {
	st, opt := d.q.store, d.q.opt
	in, err := st.GetInstance(ctx, d.id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false
	}
	if err != nil {
		d.log().Error("queue: load instance", "err", err)
		return errPause, true
	}
	if in.State != instance.Connected {
		return -1, true // paused: no attempt is consumed while disconnected
	}
	if opt.Hold != nil && opt.Hold(d.id) {
		return -1, true // sending suspended by the operator: Wake resumes
	}
	now := opt.Clock.Now()
	if ready := d.q.ready(d.id); ready.Equal(notReady) {
		return -1, true // the connected notice will wake us
	} else if now.Before(ready) {
		return ready.Sub(now), true // grace period after connecting
	}
	set, err := settings.Effective(opt.Defaults, "", in.SettingsJSON)
	if err != nil {
		d.log().Error("queue: bad settings; using defaults", "err", err)
		set = opt.Defaults
	}
	item, err := st.NextOutbound(ctx, d.id, now)
	if errors.Is(err, store.ErrNotFound) {
		due, ok, err := st.NextDue(ctx, d.id)
		switch {
		case err != nil:
			d.log().Error("queue: next due", "err", err)
			return errPause, true
		case !ok:
			return -1, true
		}
		return max(due.Sub(now), time.Millisecond), true
	}
	if err != nil {
		d.log().Error("queue: next item", "err", err)
		return errPause, true
	}
	if !item.ExpiresAt.IsZero() && now.After(item.ExpiresAt) {
		return pauseOn(d.fail(item, item.Attempts, "expired: not sent before expires_after")), true
	}
	var p Payload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: unreadable payload")), true
	}
	if msg := windowError(p, now); msg != "" {
		return pauseOn(d.fail(item, item.Attempts, msg)), true
	}
	// A reply typed with the other 9th-digit form joins its conversation before the window
	// decides whether it is cold.
	if next, done := d.cachedTarget(ctx, item); done {
		return next, true
	}
	if opens, wait, err := d.window(ctx, set, item, now); err != nil {
		d.log().Error("queue: send window", "err", err)
		return errPause, true
	} else if wait {
		if err := st.Defer(ctx, item.ID, opens, "send_window: waiting for the send window"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
	isNew, err := d.newRecipient(ctx, item)
	if err != nil {
		d.log().Error("queue: load chat", "err", err)
		return errPause, true
	}
	if until, err := d.ramp(ctx, in, set, item, isNew, now); err != nil {
		d.log().Error("queue: ramp", "err", err)
		return errPause, true
	} else if !until.IsZero() {
		if err := st.Defer(ctx, item.ID, until, "ramp_limit: daily limit of new recipients reached"); err != nil {
			d.log().Error("queue: defer", "err", err)
			return errPause, true
		}
		return 0, true
	}
	// The number lookup comes after the ramp: past today's limit new numbers are not even
	// looked up (a stream of lookups looks like contact scraping). An item moved to the
	// canonical JID is picked again, so the ramp is checked for that JID too.
	if next, done := d.resolve(ctx, in, set, item, now); done {
		return next, true
	}
	// Doubled only towards another chat: a retry of the same item is not a repeated text.
	double := isNew && p.Type == "text" && p.Text != "" && p.Text == d.lastNewText && item.ChatJID != d.lastNewChat
	if w := d.pacer.wait(now, set, double); w > 0 {
		return w, true
	}
	cli, err := d.q.workers.Client(d.id)
	if err != nil {
		return time.Second, true // connected but the worker handle is not ready yet
	}
	req, err := buildRequest(item, p)
	if err != nil {
		return pauseOn(d.fail(item, item.Attempts, "validation_failed: "+err.Error())), true
	}
	if err := st.MarkInFlight(ctx, item.ID, isNew, pendingMessage(item, p, now)); errors.Is(err, store.ErrNotFound) {
		return 0, true // canceled meanwhile
	} else if err != nil {
		d.log().Error("queue: mark in flight", "err", err)
		return errPause, true
	}
	d.presend(ctx, cli, item.ChatJID, p, set)
	if ctx.Err() != nil {
		// Shutting down before the send started: back to the queue, no attempt spent.
		if err := st.Requeue(context.Background(), item.ID, now, item.Attempts, item.LastError); err != nil {
			d.log().Error("queue: requeue", "err", err)
		}
		return 0, true
	}

	sctx, cancel := context.WithTimeout(context.Background(), opt.SendTimeout)
	resp, sendErr := cli.Send(sctx, req)
	cancel()
	d.pacer.record(opt.Clock.Now(), set)
	if isNew && p.Type == "text" {
		d.lastNewText, d.lastNewChat = p.Text, item.ChatJID
	} else {
		d.lastNewText, d.lastNewChat = "", ""
	}
	return pauseOn(d.finish(item, p, resp, sendErr)), true
}

// pauseOn is the pause after a store write: errPause when it failed (no tight loop on the
// store's single connection), else none.
func pauseOn(err error) time.Duration {
	if err != nil {
		return errPause
	}
	return 0
}

// activeWindow is how recent a received message must be for a chat to count as an active
// conversation, which the send window does not hold back.
const activeWindow = 24 * time.Hour

// window decides whether item must wait for the send window: only recipients without a
// message received in the last 24h wait (replies to active conversations go out at any
// hour). opens is when the next window opens, plus its jitter.
func (d *dispatcher) window(ctx context.Context, set settings.Settings, item store.Outbound, now time.Time) (opens time.Time, wait bool, err error) {
	start, _, open := set.WindowAt(now)
	if start.IsZero() {
		return time.Time{}, false, nil
	}
	opens = d.windowOpensAt(start)
	if open && !now.Before(opens) {
		return time.Time{}, false, nil
	}
	chat, err := d.q.store.GetChat(ctx, d.id, item.ChatJID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return time.Time{}, false, err
	}
	if !chat.LastInboundAt.IsZero() && now.Sub(chat.LastInboundAt) < activeWindow {
		return time.Time{}, false, nil
	}
	return opens, true, nil
}

// lookupBackoff is the wait after n consecutive failed lookups of an item's recipient.
func lookupBackoff(n int) time.Duration {
	switch n {
	case 1:
		return time.Minute
	case 2:
		return 2 * time.Minute
	case 3:
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

const maxLookupFailures = 5

// resolve checks a phone-number recipient the first time it is used (no chat yet): a
// number without WhatsApp fails without sending, and a number WhatsApp knows under another
// phone JID (Brazilian 9th digit) has its queued items moved there. A failed or
// rate-limited lookup only postpones this item; other recipients keep going. done=true
// means the item was handled (failed, deferred or moved) and the step should sleep next
// (errPause after a failed store write) and pick again.
//
// A lookup that has to ask WhatsApp is paced like a send (and recorded in the pacer), and
// the instance makes at most the ramp's daily cap of them per day (the ramp only counts
// sends; a run of numbers without WhatsApp would otherwise be looked up without limit).
func (d *dispatcher) resolve(ctx context.Context, in store.Instance, set settings.Settings, item store.Outbound, now time.Time) (next time.Duration, done bool) {
	user, ok, err := d.unknownPhone(ctx, item)
	if err != nil {
		d.log().Error("queue: load chat", "err", err)
		return errPause, true
	}
	if !ok {
		return 0, false
	}
	r, st := d.q.opt.Resolver, d.q.store
	if res, ok, err := r.Cached(ctx, d.id, user); err != nil {
		d.log().Error("queue: number cache", "err", err)
		return errPause, true
	} else if ok {
		return d.applyLookup(ctx, item, res)
	}
	if limit := set.Ramp.DailyCap(in.FirstConnectedAt, now); limit >= 0 {
		n, err := st.LookupsSince(ctx, d.id, set.DayStart(now))
		if err != nil {
			d.log().Error("queue: count lookups", "err", err)
			return errPause, true
		}
		if n >= limit {
			err := st.Defer(ctx, item.ID, d.nextDayWindow(set, now), "lookup_daily_limit: daily limit of number lookups reached")
			if err != nil {
				d.log().Error("queue: defer", "err", err)
			}
			return pauseOn(err), true
		}
	}
	if w := d.pacer.wait(now, set, false); w > 0 {
		return w, true
	}
	res, network, err := r.Lookup(ctx, d.id, []string{user}, lookup.ForSend)
	if network {
		d.pacer.record(d.q.opt.Clock.Now(), set)
	}
	switch {
	case ctx.Err() != nil:
		return 0, true // shutting down: an interrupted lookup is not a lookup failure
	case errors.Is(err, lookup.ErrRateLimited):
		err := st.Defer(ctx, item.ID, now.Add(time.Minute), "lookup_rate_limited: retrying in a minute")
		if err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return pauseOn(err), true
	case errors.Is(err, manager.ErrNotRunning) || status.Code(err) == codes.Unavailable:
		// Not the number's fault: wait for the worker without counting a lookup failure.
		err := st.Defer(ctx, item.ID, now.Add(errPause), "instance_not_connected: the number lookup could not reach the worker")
		if err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return pauseOn(err), true
	case err != nil:
		n := item.LookupFailures + 1
		msg := "worker_error: recipient lookup failed: " + status.Convert(err).Message()
		if n >= maxLookupFailures {
			return pauseOn(d.fail(item, item.Attempts, msg)), true
		}
		err := st.DeferLookup(ctx, item.ID, now.Add(lookupBackoff(n)), n, msg)
		if err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return pauseOn(err), true
	}
	return d.applyLookup(ctx, item, res[0])
}

// unknownPhone returns the phone number of item's recipient when it may need a lookup: a
// resolver is set, the recipient is a phone JID and there is no chat with it yet.
func (d *dispatcher) unknownPhone(ctx context.Context, item store.Outbound) (string, bool, error) {
	user, ok := strings.CutSuffix(item.ChatJID, "@s.whatsapp.net")
	if d.q.opt.Resolver == nil || !ok {
		return "", false, nil
	}
	if _, err := d.q.store.GetChat(ctx, d.id, item.ChatJID); err == nil {
		return "", false, nil // known chat: we already talked to this JID
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	return user, true, nil
}

// cachedTarget applies a fresh cached lookup of a recipient without a chat, before the
// send window and the ramp look at it: the other 9th-digit form of a number moves the item
// to the conversation that already exists, and a number known not to be on WhatsApp fails
// at once. Cache only: no network, no quota.
func (d *dispatcher) cachedTarget(ctx context.Context, item store.Outbound) (next time.Duration, done bool) {
	user, ok, err := d.unknownPhone(ctx, item)
	if err != nil {
		d.log().Error("queue: load chat", "err", err)
		return errPause, true
	}
	if !ok {
		return 0, false
	}
	res, ok, err := d.q.opt.Resolver.Cached(ctx, d.id, user)
	if err != nil {
		d.log().Error("queue: number cache", "err", err)
		return errPause, true
	}
	if !ok {
		return 0, false
	}
	return d.applyLookup(ctx, item, res)
}

// applyLookup acts on a lookup result: not on WhatsApp → failed; another phone JID → the
// chat's queued items move there (done, pick again); otherwise the send goes ahead.
func (d *dispatcher) applyLookup(ctx context.Context, item store.Outbound, res lookup.Result) (next time.Duration, done bool) {
	switch {
	case !res.Found:
		return pauseOn(d.fail(item, item.Attempts, "invalid_number: recipient is not on WhatsApp")), true
	case res.JID != item.ChatJID && strings.HasSuffix(res.JID, "@s.whatsapp.net"):
		// Only to another phone JID: a LID answer keeps the phone JID (chats are by phone).
		if err := d.q.store.RetargetChat(ctx, d.id, item.ChatJID, res.JID); err != nil {
			d.log().Error("queue: retarget", "err", err)
			return errPause, true
		}
		return 0, true
	}
	return 0, false
}

// newRecipient reports whether item goes to a chat (groups included) that never sent us
// anything. Such sends count against the ramp, and an identical text to new recipients in
// a row doubles the interval, whether or not the ramp is enabled.
func (d *dispatcher) newRecipient(ctx context.Context, item store.Outbound) (bool, error) {
	chat, err := d.q.store.GetChat(ctx, d.id, item.ChatJID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return !chat.HasInbound, nil
}

// ramp returns when item must be deferred to because today's limit of new recipients is
// reached (zero: it may go now). Each new chat counts once a day.
func (d *dispatcher) ramp(ctx context.Context, in store.Instance, set settings.Settings, item store.Outbound, isNew bool, now time.Time) (time.Time, error) {
	if !set.Ramp.Enabled || !isNew {
		return time.Time{}, nil
	}
	day := set.DayStart(now)
	count, counted, err := d.q.store.NewRecipientsSince(ctx, d.id, item.ChatJID, day)
	if err != nil || counted || count < set.Ramp.DailyCap(in.FirstConnectedAt, now) {
		return time.Time{}, err
	}
	return d.nextDayWindow(set, now), nil
}

// nextDayWindow is where daily limits defer to: not midnight but the next day's window
// start plus jitter (a burst of deferred messages at night looks like spam).
func (d *dispatcher) nextDayWindow(set settings.Settings, now time.Time) time.Time {
	tomorrow := set.DayStart(now).AddDate(0, 0, 1)
	if start, _, _ := set.WindowAt(tomorrow); !start.IsZero() {
		tomorrow = start
	}
	return tomorrow.Add(time.Duration(d.q.opt.Rand() * float64(d.q.opt.WindowJitter)))
}

func buildRequest(item store.Outbound, p Payload) (*workerv1.SendRequest, error) {
	req := &workerv1.SendRequest{
		To: item.ChatJID, MessageId: item.WAMessageID, Type: p.Type, Text: p.Text,
		Mime: p.Mime, FileName: p.FileName, Ptt: p.PTT,
	}
	if p.MediaFile != "" {
		if _, err := os.Stat(p.MediaFile); err != nil {
			return nil, errors.New("staged media is missing")
		}
		req.MediaPath = p.MediaFile // the worker streams it from disk
	}
	if r := p.Reaction; r != nil {
		req.ReactionMessageId, req.ReactionFromMe, req.ReactionSender = r.WAMessageID, r.FromMe, r.Sender
	}
	req.Animated, req.MentionAll, req.Unpin, req.PinSeconds = p.Animated, p.MentionAll, p.Unpin, p.PinSeconds
	req.ForwardKind, req.ForwardRef = p.ForwardKind, p.ForwardRef
	if m := p.Meta; m != nil {
		req.Mentions, req.Forwarded = m.Mentions, m.Forwarded
		if l := m.Location; l != nil {
			req.Location = &workerv1.Location{Latitude: l.Latitude, Longitude: l.Longitude, Name: l.Name, Address: l.Address, Url: l.URL}
		}
		for _, c := range m.Contacts {
			req.Contacts = append(req.Contacts, &workerv1.ContactCard{DisplayName: c.DisplayName, Vcard: c.VCard})
		}
		if pl := m.Poll; pl != nil {
			req.Poll = &workerv1.Poll{Name: pl.Name, Options: pl.Options, Selectable: uint32(pl.Selectable)}
		}
	}
	if q := p.Quote; q != nil {
		req.Quote = &workerv1.Quote{MessageId: q.WAMessageID, FromMe: q.FromMe, Sender: q.Sender, Text: q.Text, RemoteJid: q.RemoteJID}
	}
	if st := p.StatusStyle; st != nil {
		req.StatusStyle = &workerv1.StatusStyle{BackgroundArgb: st.BackgroundARGB, TextArgb: st.TextARGB, Font: st.Font}
	}
	if lp := p.LinkPreview; lp != nil {
		req.LinkPreview = &workerv1.LinkPreview{Url: lp.URL, Title: lp.Title, Description: lp.Description, Thumbnail: lp.Thumbnail}
	}
	if t := p.Target; t != nil {
		req.TargetId, req.TargetFromMe, req.TargetSender = t.WAMessageID, t.FromMe, t.Sender
	}
	return req, nil
}

// presend waits the message's delay_ms, then shows "typing..." (or "recording..." for
// voice notes) before a send: for typing_ms when the message asks for it, else as the
// typing simulation setting says. Both only add time after the pacing check.
func (d *dispatcher) presend(ctx context.Context, cli workerv1.WorkerClient, chat string, p Payload, set settings.Settings) {
	if p.DelayMS > 0 && !d.pause(ctx, time.Duration(p.DelayMS)*time.Millisecond) {
		return
	}
	if strings.HasSuffix(chat, "@broadcast") || strings.HasSuffix(chat, "@newsletter") {
		return // nobody sees "typing..." on a status or a channel post
	}
	state, delay := "", time.Duration(0)
	switch {
	case p.Type == "audio" && p.PTT:
		state, delay = "recording", 2*time.Second
	case p.Type == "text":
		state, delay = "composing", typingDelay(p.Text)
	case p.TypingMS > 0 && !Updates(p.Type) && p.Type != "reaction":
		state = "composing"
	}
	if p.TypingMS > 0 && state != "" {
		delay = time.Duration(p.TypingMS) * time.Millisecond
	} else if !set.TypingSimulation {
		return
	}
	if state == "" {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = cli.SendPresence(pctx, &workerv1.SendPresenceRequest{To: chat, State: state})
	cancel()
	d.pause(ctx, delay)
}

// pause sleeps on the queue clock; false when ctx ended first.
func (d *dispatcher) pause(ctx context.Context, dur time.Duration) bool {
	t := d.q.opt.Clock.NewTimer(dur)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-ctx.Done():
		return false
	}
}

// finish records the outcome of a send attempt; an error means the store write failed.
func (d *dispatcher) finish(item store.Outbound, p Payload, resp *workerv1.SendResponse, sendErr error) error {
	ctx := context.Background()
	st, opt := d.q.store, d.q.opt
	if sendErr == nil {
		at := opt.Clock.Now()
		if resp.GetTimestampMs() > 0 {
			at = time.UnixMilli(resp.GetTimestampMs())
		}
		return d.sent(item, p, at)
	}
	// A receipt already proved delivery (the worker's answer was lost or late): it was sent.
	if m, err := st.GetMessage(ctx, d.id, item.ID); err == nil && (m.Status == "delivered" || m.Status == "read" || m.Status == "played") {
		return d.sent(item, p, opt.Clock.Now())
	}
	s := status.Convert(sendErr)
	msg := s.Message()
	if !strings.Contains(msg, ": ") {
		msg = "worker_error: " + msg
	}
	attempts := item.Attempts + 1
	if strings.HasPrefix(msg, "instance_not_connected:") {
		// The worker itself says it is not connected: wait without spending an attempt,
		// whatever the stored state says (it may lag behind).
		return d.requeue(item, opt.Clock.Now().Add(errPause), item.Attempts, msg)
	}
	switch s.Code() {
	case codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition:
		return d.fail(item, attempts, msg)
	case codes.Unavailable, codes.Canceled:
		// Not connected: unless the instance still claims to be connected, wait for the
		// reconnection without consuming an attempt.
		if in, err := st.GetInstance(ctx, d.id); err == nil && in.State != instance.Connected {
			return d.requeue(item, opt.Clock.Now(), item.Attempts, msg)
		}
	case codes.DeadlineExceeded:
		msg = "worker_timeout: " + strings.TrimPrefix(msg, "worker_timeout: ")
	}
	if attempts >= opt.MaxAttempts {
		return d.fail(item, attempts, msg)
	}
	return d.requeue(item, opt.Clock.Now().Add(retryDelay(attempts)), attempts, msg)
}

func (d *dispatcher) requeue(item store.Outbound, next time.Time, attempts int, msg string) error {
	err := d.q.store.Requeue(context.Background(), item.ID, next, attempts, msg)
	if err != nil {
		d.log().Error("queue: requeue", "err", err)
	}
	return err
}

// sent finishes a delivered item. If the write fails the item stays in flight (boot
// recovery requeues it; the reused wa_message_id keeps the resend idempotent), so the
// staged media is kept.
func (d *dispatcher) sent(item store.Outbound, p Payload, at time.Time) error {
	if err := d.q.store.MarkSent(context.Background(), item.ID, at); err != nil {
		d.log().Error("queue: mark sent", "err", err)
		return err
	}
	d.applyUpdate(item, p, at)
	if pub := d.q.opt.Events; pub != nil {
		if !Updates(p.Type) {
			m := events.Message{ID: item.ID, WAMessageID: item.WAMessageID, Chat: item.ChatJID, FromMe: true,
				Source: "api", Type: logType(p), Text: p.Text, Timestamp: at.UTC()}
			if p.Reaction != nil {
				m.ReactionTo = p.Reaction.WAMessageID
			}
			if hasMedia(p) {
				m.Media = &events.Media{Kind: mediaKind(logType(p)), Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT}
			}
			if p.Meta != nil {
				m.MessageMeta = *p.Meta
			}
			pub.Publish(d.id, events.TypeMessageSent, m)
		}
		// A receipt that beat this confirmation already published delivered/read/played: a later
		// "sent" would move the status backwards for consumers.
		if logged, err := d.q.store.GetMessage(context.Background(), d.id, item.ID); err != nil ||
			(logged.Status != "delivered" && logged.Status != "read" && logged.Status != "played") {
			pub.Publish(d.id, events.TypeMessageStatus, events.MessageStatus{ID: item.ID, WAMessageID: item.WAMessageID,
				Chat: item.ChatJID, Status: store.OutSent})
		}
	}
	d.done(item, p)
	return nil
}

// applyUpdate applies a sent edit or revoke to the target message in the log and
// publishes message.edited / message.revoked.
func (d *dispatcher) applyUpdate(item store.Outbound, p Payload, at time.Time) {
	if p.Target == nil || (p.Type != "edit" && p.Type != "revoke") {
		return
	}
	ctx := context.Background()
	var m store.Message
	var err error
	typ := events.TypeMessageEdited
	if p.Type == "edit" {
		m, err = d.q.store.EditMessage(ctx, d.id, item.ChatJID, p.Target.WAMessageID, p.Text, at)
	} else {
		typ = events.TypeMessageRevoked
		m, err = d.q.store.RevokeMessage(ctx, d.id, item.ChatJID, p.Target.WAMessageID, at)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		d.log().Error("queue: apply "+p.Type, "err", err)
	}
	if pub := d.q.opt.Events; pub != nil {
		u := events.MessageUpdate{ID: m.ID, WAMessageID: p.Target.WAMessageID, Chat: item.ChatJID, FromMe: true,
			Source: "api", Timestamp: at.UTC()}
		if p.Type == "edit" {
			u.Text = p.Text
		}
		pub.Publish(d.id, typ, u)
	}
}

// mediaKind is the attachment kind of a send type (gif and ptv are videos).
func mediaKind(typ string) string {
	if typ == "gif" || typ == "ptv" {
		return "video"
	}
	return typ
}

// logType is the type a sent message is logged with: a forward takes its content's type.
func logType(p Payload) string {
	if p.Type == "forward" && p.ForwardKind != "" {
		return p.ForwardKind
	}
	return p.Type
}

// hasMedia reports whether the message carries an attachment (staged, or forwarded as is).
func hasMedia(p Payload) bool {
	return p.MediaFile != "" || (p.Type == "forward" && p.ForwardKind != "")
}

// fail finishes an item for good. If the write fails the item keeps its status (queued,
// or in_flight when it fails after a send attempt) and its staged media.
func (d *dispatcher) fail(item store.Outbound, attempts int, msg string) error {
	if err := d.q.store.MarkFailed(context.Background(), item.ID, attempts, msg); err != nil {
		d.log().Error("queue: mark failed", "err", err)
		return err
	}
	if pub := d.q.opt.Events; pub != nil {
		pub.Publish(d.id, events.TypeMessageStatus, events.MessageStatus{ID: item.ID, Chat: item.ChatJID,
			Status: store.OutFailed, Error: msg})
	}
	var p Payload
	_ = json.Unmarshal([]byte(item.Payload), &p)
	d.done(item, p)
	return nil
}

// done cleans up a final item and wakes its waiters.
func (d *dispatcher) done(item store.Outbound, p Payload) {
	if p.MediaFile != "" {
		_ = os.Remove(p.MediaFile)
	}
	d.q.notify(d.id, item.ID)
}

// pendingMessage is the log row written when the item goes in flight (nil for edits,
// revokes and pins: they change another message).
func pendingMessage(item store.Outbound, p Payload, at time.Time) *store.Message {
	if Updates(p.Type) {
		return nil
	}
	m := &store.Message{
		ID: item.ID, WAMessageID: item.WAMessageID, ChatJID: item.ChatJID, FromMe: true, Source: "api",
		Type: logType(p), Text: p.Text, Status: "pending", Timestamp: at,
	}
	if p.Reaction != nil {
		m.ReactionTo = p.Reaction.WAMessageID
	}
	if hasMedia(p) {
		b, _ := json.Marshal(store.MediaInfo{Kind: mediaKind(logType(p)), Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT})
		m.MediaJSON = string(b)
	}
	if p.Meta != nil {
		m.MetaJSON = p.Meta.JSON()
	}
	return m
}
