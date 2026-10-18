// Package realtime turns the worker's call and presence signals into events (marco 7):
// chat.presence, call.received, call.accepted and call.ended. It applies the call policy
// of each instance (calls.auto_reject, calls.reject_message) and its own online status
// (presence.online). It is a manager.Observer.
package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
)

// Workers gives access to a running worker (manager.Manager).
type Workers interface {
	Client(id string) (workerv1.WorkerClient, error)
}

// Sender queues a message (service.GuardedMessaging: pacing, window, ramp and the
// operator's suspension all apply).
type Sender interface {
	Send(ctx context.Context, instanceID string, in service.SendInput, wait time.Duration) (service.MessageView, error)
}

// SettingsFunc returns an instance's effective settings.
type SettingsFunc func(ctx context.Context, instanceID string) (settings.Settings, error)

const (
	rpcTimeout = 3 * time.Second // the spec wants an automatic rejection in < 3s
	// replyCooldown: at most one reject_message per caller in this period (a caller who
	// keeps calling does not make the instance spam them).
	replyCooldown = 10 * time.Minute
	// replyExpiry: a reject_message that cannot go out by then (send window closed, long
	// queue) is dropped as expired instead of arriving hours after the call.
	replyExpiry = "1h"
	callMemory  = time.Hour // calls are remembered this long (to dedupe and tell missed calls)
	maxCalls    = 1024
	// presenceDedupe drops a repeated identical typing state (WhatsApp resends composing
	// every few seconds while the contact types).
	presenceDedupe = 5 * time.Second
	maxPresence    = 4096
)

type callState struct {
	seen     time.Time
	chat     string
	caller   string
	group    bool
	media    string
	accepted bool
	rejected bool // by this instance (auto_reject)
	ended    bool
}

type presenceState struct {
	state string
	at    time.Time
}

// Handler is the observer. Workers, Settings and Events are required; the Sender (set
// with SetSender, possibly after workers started) only for reject messages.
type Handler struct {
	Workers  Workers
	Settings SettingsFunc
	Events   events.Publisher
	Log      *slog.Logger
	Clock    clock.Clock

	mu       sync.Mutex
	sender   Sender
	calls    map[string]*callState    // instance|call id
	replied  map[string]time.Time     // instance|caller -> last reject message
	presence map[string]presenceState // instance|chat|sender -> last typing state
	wg       sync.WaitGroup           // background RPCs (Wait, for tests and shutdown)
}

func (h *Handler) now() time.Time {
	if h.Clock == nil {
		return time.Now()
	}
	return h.Clock.Now()
}

func (h *Handler) log() *slog.Logger {
	if h.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return h.Log
}

// SetSender sets where reject messages are queued.
func (h *Handler) SetSender(s Sender) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sender = s
}

// Wait blocks until background work (presence RPCs) has finished.
func (h *Handler) Wait() { h.wg.Wait() }

// StateChanged marks the account online again after every (re)connection when
// presence.online is on: WhatsApp forgets it with the socket.
func (h *Handler) StateChanged(id string, st instance.State, _ string) {
	if st != instance.Connected {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		set, err := h.Settings(ctx, id)
		if err != nil || !set.Presence.Online {
			return
		}
		h.setPresence(ctx, id, true)
	}()
}

// SettingsChanged applies a change of presence.online right away (a running worker;
// otherwise it is applied on the next connection).
func (h *Handler) SettingsChanged(id string, before, after settings.Settings) {
	if before.Presence.Online == after.Presence.Online {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		h.setPresence(ctx, id, after.Presence.Online)
	}()
}

func (h *Handler) setPresence(ctx context.Context, id string, available bool) {
	cli, err := h.Workers.Client(id)
	if err != nil {
		return // not running: StateChanged applies it when it connects
	}
	if _, err := cli.SetPresence(ctx, &workerv1.SetPresenceRequest{Available: available}); err != nil {
		h.log().Warn("set presence", "instance_id", id, "available", available, "err", err)
	}
}

func (h *Handler) WorkerEvent(id string, ev *workerv1.Event) {
	switch {
	case ev.GetChatPresence() != nil:
		p := ev.GetChatPresence()
		h.publishPresence(id, events.ChatPresence{Chat: p.Chat, Sender: p.Sender, IsGroup: p.IsGroup, State: p.State})
	case ev.GetPresence() != nil:
		p := ev.GetPresence()
		cp := events.ChatPresence{Chat: p.Jid, Sender: p.Jid, State: "offline"}
		if p.Online {
			cp.State = "online"
		} else if p.LastSeenMs > 0 {
			t := time.UnixMilli(p.LastSeenMs).UTC()
			cp.LastSeen = &t
		}
		h.publishPresence(id, cp)
	case ev.GetCall() != nil:
		h.call(id, ev.GetCall())
	}
}

func (h *Handler) publishPresence(id string, p events.ChatPresence) {
	if p.Chat == "" {
		return
	}
	key := id + "|" + p.Chat + "|" + p.Sender
	now := h.now()
	h.mu.Lock()
	if h.presence == nil || len(h.presence) >= maxPresence {
		h.presence = map[string]presenceState{}
	}
	last, ok := h.presence[key]
	dup := ok && last.state == p.State && now.Sub(last.at) < presenceDedupe
	if !dup {
		h.presence[key] = presenceState{state: p.State, at: now}
	}
	h.mu.Unlock()
	if !dup {
		h.Events.Publish(id, events.TypeChatPresence, p)
	}
}

// track returns the call's state, creating it; must hold h.mu.
func (h *Handler) track(key string, now time.Time) *callState {
	if h.calls == nil {
		h.calls = map[string]*callState{}
	}
	if c, ok := h.calls[key]; ok {
		return c
	}
	for k, c := range h.calls {
		if now.Sub(c.seen) > callMemory || len(h.calls) >= maxCalls {
			delete(h.calls, k)
		}
	}
	c := &callState{seen: now}
	h.calls[key] = c
	return c
}

func (h *Handler) call(id string, c *workerv1.Call) {
	if c.CallId == "" {
		return
	}
	now := h.now()
	at := time.UnixMilli(c.TimestampMs).UTC()
	if c.TimestampMs == 0 {
		at = now.UTC()
	}
	key := id + "|" + c.CallId
	switch c.Kind {
	case "offer", "offer_notice":
		h.mu.Lock()
		st := h.track(key, now)
		fresh := st.chat == ""
		st.caller, st.group, st.media = c.Caller, c.Group != "", c.Media
		st.chat = c.Caller
		if st.group {
			st.chat = c.Group
		}
		h.mu.Unlock()
		if fresh {
			h.offer(id, key, c, at)
		}
	case "accept":
		h.mu.Lock()
		st := h.track(key, now)
		first := !st.accepted && !st.ended
		st.accepted = true
		data := h.data(st, c, at)
		h.mu.Unlock()
		if first {
			h.Events.Publish(id, events.TypeCallAccepted, data)
		}
	case "terminate", "reject":
		reason := c.Reason
		if c.Kind == "reject" {
			reason = "rejected_by_device"
		}
		h.end(id, key, c, at, reason)
	}
}

// data builds the event data of a call from what is known about it; must hold h.mu.
func (h *Handler) data(st *callState, c *workerv1.Call, at time.Time) events.Call {
	d := events.Call{CallID: c.CallId, Chat: st.chat, Caller: st.caller, IsGroup: st.group, Media: st.media, Timestamp: at}
	if d.Caller == "" { // a signal for a call we did not see ringing (worker restarted)
		d.Caller, d.Chat = c.Caller, c.Caller
		if c.Group != "" {
			d.Chat, d.IsGroup = c.Group, true
		}
	}
	return d
}

// offer publishes call.received, rejecting the call first when calls.auto_reject is on.
// The rejection runs synchronously (bounded by rpcTimeout) so call.received always comes
// before call.ended.
func (h *Handler) offer(id, key string, c *workerv1.Call, at time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	set, err := h.Settings(ctx, id)
	if err != nil {
		h.log().Error("call policy: load settings", "instance_id", id, "err", err)
	}
	rejected := false
	if err == nil && set.Calls.AutoReject {
		if cli, cerr := h.Workers.Client(id); cerr != nil {
			h.log().Warn("auto reject: worker not running", "instance_id", id)
		} else if _, rerr := cli.RejectCall(ctx, &workerv1.RejectCallRequest{CallId: c.CallId}); rerr != nil {
			h.log().Warn("auto reject failed", "instance_id", id, "err", rerr)
		} else {
			rejected = true
		}
	}
	h.mu.Lock()
	st := h.track(key, h.now())
	st.rejected = rejected
	data := h.data(st, c, at)
	h.mu.Unlock()
	data.Rejected = rejected
	h.Events.Publish(id, events.TypeCallReceived, data)
	if !rejected {
		return
	}
	// WhatsApp may or may not echo a terminate for a call we rejected: end it here, once.
	h.end(id, key, c, at, "rejected")
	if msg := set.Calls.RejectMessage; msg != "" && c.Group == "" && c.Caller != "" {
		h.reply(id, c, msg)
	}
}

// end publishes call.ended once per call.
func (h *Handler) end(id, key string, c *workerv1.Call, at time.Time, reason string) {
	h.mu.Lock()
	st := h.track(key, h.now())
	if st.ended {
		h.mu.Unlock()
		return
	}
	st.ended = true
	data := h.data(st, c, at)
	if st.rejected {
		reason = "rejected"
	}
	data.Missed = !st.accepted && !st.rejected && reason != "rejected_by_device"
	h.mu.Unlock()
	data.Reason = reason
	h.Events.Publish(id, events.TypeCallEnded, data)
}

// reply queues calls.reject_message to the caller, at most once per replyCooldown.
func (h *Handler) reply(id string, c *workerv1.Call, msg string) {
	now := h.now()
	key := id + "|" + c.Caller
	h.mu.Lock()
	sender := h.sender
	if sender == nil {
		h.mu.Unlock()
		return
	}
	if h.replied == nil {
		h.replied = map[string]time.Time{}
	}
	for k, t := range h.replied {
		if now.Sub(t) >= replyCooldown {
			delete(h.replied, k)
		}
	}
	if _, recent := h.replied[key]; recent {
		h.mu.Unlock()
		return
	}
	h.replied[key] = now
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	// The client id makes a replayed offer (worker reconnect) queue nothing new.
	_, err := sender.Send(ctx, id, service.SendInput{To: c.Caller, Type: "text", Text: msg,
		ClientMessageID: "call-reject-" + c.CallId, ExpiresAfter: replyExpiry}, 0)
	if err != nil {
		h.log().Warn("queue call reject message", "instance_id", id, "err", err)
	}
}
