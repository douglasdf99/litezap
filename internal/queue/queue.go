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
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Payload is what the service stores in outbound_queue.payload_json.
type Payload struct {
	Type string `json:"type"` // text | image | video | audio | document | reaction
	Text string `json:"text,omitempty"`
	// MediaFile is the staged attachment; it is deleted once the item is final.
	MediaFile string          `json:"media_file,omitempty"`
	Mime      string          `json:"mime,omitempty"`
	FileName  string          `json:"file_name,omitempty"`
	Size      int64           `json:"size,omitempty"`
	PTT       bool            `json:"ptt,omitempty"`
	Reaction  *ReactionTarget `json:"reaction,omitempty"`
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
	// WindowJitter spreads ramp-deferred sends over [0, WindowJitter) after the next
	// window start (default 5 minutes).
	WindowJitter time.Duration
	Log          *slog.Logger
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
	waiters map[string][]chan struct{}
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
		disp: map[string]*dispatcher{}, waiters: map[string][]chan struct{}{}}
}

// Recover runs at boot, before any worker starts: in_flight items go back to queued.
func (q *Queues) Recover(ctx context.Context) (int, error) { return q.store.RecoverInFlight(ctx) }

func (q *Queues) WorkerEvent(string, *workerv1.Event) {}

func (q *Queues) StateChanged(id string, st instance.State) {
	if st == instance.Connected {
		q.Wake(id)
	}
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
	for {
		ch := make(chan struct{})
		q.mu.Lock()
		q.waiters[id] = append(q.waiters[id], ch)
		q.mu.Unlock()
		o, err := q.store.GetOutbound(ctx, instanceID, id)
		if err != nil || final(o.Status) {
			q.dropWaiter(id, ch)
			return o, err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			q.dropWaiter(id, ch)
			// The request context is over, not the store: read with a fresh one.
			o, err = q.store.GetOutbound(context.WithoutCancel(ctx), instanceID, id)
			return o, err
		}
	}
}

func (q *Queues) dropWaiter(id string, ch chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.waiters[id]
	for i, c := range l {
		if c == ch {
			q.waiters[id] = append(l[:i], l[i+1:]...)
			break
		}
	}
	if len(q.waiters[id]) == 0 {
		delete(q.waiters, id)
	}
}

// notify wakes the Wait calls of an item whose status changed.
func (q *Queues) notify(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.waiters[id] {
		close(ch)
	}
	delete(q.waiters, id)
}

type dispatcher struct {
	q     *Queues
	id    string
	wake  chan struct{}
	pacer pacer
	// lastNewText is the text of the previous send when it went to a new recipient.
	lastNewText string
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
	now := opt.Clock.Now()
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
	var p Payload
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		d.fail(item, item.Attempts, "validation_failed: unreadable payload")
		return 0, true
	}

	isNew, deferUntil, err := d.ramp(ctx, in, set, item, now)
	if err != nil {
		d.log().Error("queue: ramp", "err", err)
		return errPause, true
	}
	if !deferUntil.IsZero() {
		if err := st.Defer(ctx, item.ID, deferUntil, "ramp_limit: daily limit of new recipients reached"); err != nil {
			d.log().Error("queue: defer", "err", err)
		}
		return 0, true
	}
	double := isNew && p.Type == "text" && p.Text != "" && p.Text == d.lastNewText
	if w := d.pacer.wait(now, set, double); w > 0 {
		return w, true
	}
	cli, err := d.q.workers.Client(d.id)
	if err != nil {
		return time.Second, true // connected but the worker handle is not ready yet
	}
	req, err := buildRequest(item, p)
	if err != nil {
		d.fail(item, item.Attempts, "validation_failed: "+err.Error())
		return 0, true
	}
	if err := st.MarkInFlight(ctx, item.ID, isNew, pendingMessage(item, p, now)); err != nil {
		d.log().Error("queue: mark in flight", "err", err)
		return errPause, true
	}
	d.typing(ctx, cli, item.ChatJID, p, set)
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
		d.lastNewText = p.Text
	} else {
		d.lastNewText = ""
	}
	d.finish(item, p, resp, sendErr)
	return 0, true
}

// ramp decides whether item goes to a new recipient (counting against today's limit) or
// must wait for the next day's send window (deferUntil set). Chats (groups included) that
// already sent us something never count.
func (d *dispatcher) ramp(ctx context.Context, in store.Instance, set settings.Settings, item store.Outbound, now time.Time) (isNew bool, deferUntil time.Time, err error) {
	if !set.Ramp.Enabled {
		return false, time.Time{}, nil
	}
	chat, err := d.q.store.GetChat(ctx, d.id, item.ChatJID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, time.Time{}, err
	}
	if chat.HasInbound {
		return false, time.Time{}, nil
	}
	day := set.DayStart(now)
	count, counted, err := d.q.store.NewRecipientsSince(ctx, d.id, item.ChatJID, day)
	if err != nil || counted {
		return false, time.Time{}, err
	}
	if count < set.Ramp.DailyCap(in.FirstConnectedAt, now) {
		return true, time.Time{}, nil
	}
	// Not at midnight: at the next day's window start plus jitter (a burst of deferred
	// messages at night looks like spam).
	tomorrow := day.AddDate(0, 0, 1)
	if start, _, _ := set.WindowAt(tomorrow); !start.IsZero() {
		tomorrow = start
	}
	return false, tomorrow.Add(time.Duration(d.q.opt.Rand() * float64(d.q.opt.WindowJitter))), nil
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
	return req, nil
}

// typing shows "typing..." (or "recording..." for voice notes) before a send.
func (d *dispatcher) typing(ctx context.Context, cli workerv1.WorkerClient, chat string, p Payload, set settings.Settings) {
	if !set.TypingSimulation {
		return
	}
	var state string
	var delay time.Duration
	switch {
	case p.Type == "text":
		state, delay = "composing", typingDelay(p.Text)
	case p.Type == "audio" && p.PTT:
		state, delay = "recording", 2*time.Second
	default:
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = cli.SendPresence(pctx, &workerv1.SendPresenceRequest{To: chat, State: state})
	cancel()
	t := d.q.opt.Clock.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C():
	case <-ctx.Done():
	}
}

// finish records the outcome of a send attempt.
func (d *dispatcher) finish(item store.Outbound, p Payload, resp *workerv1.SendResponse, sendErr error) {
	ctx := context.Background()
	st, opt := d.q.store, d.q.opt
	if sendErr == nil {
		at := opt.Clock.Now()
		if resp.GetTimestampMs() > 0 {
			at = time.UnixMilli(resp.GetTimestampMs())
		}
		if err := st.MarkSent(ctx, item.ID, at); err != nil {
			d.log().Error("queue: mark sent", "err", err)
		}
		d.done(item, p)
		return
	}
	// A receipt already proved delivery (the worker's answer was lost or late): it was sent.
	if m, err := st.GetMessage(ctx, d.id, item.ID); err == nil && (m.Status == "delivered" || m.Status == "read") {
		if err := st.MarkSent(ctx, item.ID, opt.Clock.Now()); err != nil {
			d.log().Error("queue: mark sent", "err", err)
		}
		d.done(item, p)
		return
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
		if err := st.Requeue(ctx, item.ID, opt.Clock.Now().Add(errPause), item.Attempts, msg); err != nil {
			d.log().Error("queue: requeue", "err", err)
		}
		return
	}
	switch s.Code() {
	case codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition:
		d.fail(item, attempts, msg)
		return
	case codes.Unavailable, codes.Canceled:
		// Not connected: unless the instance still claims to be connected, wait for the
		// reconnection without consuming an attempt.
		if in, err := st.GetInstance(ctx, d.id); err == nil && in.State != instance.Connected {
			if err := st.Requeue(ctx, item.ID, opt.Clock.Now(), item.Attempts, msg); err != nil {
				d.log().Error("queue: requeue", "err", err)
			}
			return
		}
	case codes.DeadlineExceeded:
		msg = "worker_timeout: " + strings.TrimPrefix(msg, "worker_timeout: ")
	}
	if attempts >= opt.MaxAttempts {
		d.fail(item, attempts, msg)
		return
	}
	if err := st.Requeue(ctx, item.ID, opt.Clock.Now().Add(retryDelay(attempts)), attempts, msg); err != nil {
		d.log().Error("queue: requeue", "err", err)
	}
}

func (d *dispatcher) fail(item store.Outbound, attempts int, msg string) {
	if err := d.q.store.MarkFailed(context.Background(), item.ID, attempts, msg); err != nil {
		d.log().Error("queue: mark failed", "err", err)
	}
	var p Payload
	_ = json.Unmarshal([]byte(item.Payload), &p)
	d.done(item, p)
}

// done cleans up a final item and wakes its waiters.
func (d *dispatcher) done(item store.Outbound, p Payload) {
	if p.MediaFile != "" {
		_ = os.Remove(p.MediaFile)
	}
	d.q.notify(item.ID)
}

// pendingMessage is the log row written when the item goes in flight.
func pendingMessage(item store.Outbound, p Payload, at time.Time) store.Message {
	m := store.Message{
		ID: item.ID, WAMessageID: item.WAMessageID, ChatJID: item.ChatJID, FromMe: true, Source: "api",
		Type: p.Type, Text: p.Text, Status: "pending", Timestamp: at,
	}
	if p.Reaction != nil {
		m.ReactionTo = p.Reaction.WAMessageID
	}
	if p.MediaFile != "" {
		b, _ := json.Marshal(store.MediaInfo{Kind: p.Type, Mime: p.Mime, Size: p.Size, FileName: p.FileName, PTT: p.PTT})
		m.MediaJSON = string(b)
	}
	return m
}
