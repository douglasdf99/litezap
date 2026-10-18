package agenthook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/clock"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
)

// Messenger is what the hook needs to act: the guarded send path (queue + operator
// suspension) and read receipts. service.GuardedMessaging implements it.
type Messenger interface {
	Send(ctx context.Context, instanceID string, in service.SendInput, wait time.Duration) (service.MessageView, error)
	MarkRead(ctx context.Context, instanceID, chat string) error
	// CancelAgentReplies drops the hook's replies still queued for a chat.
	CancelAgentReplies(ctx context.Context, instanceID, chat, reason string) (int, error)
}

type Options struct {
	Clock clock.Clock
	// AllowPrivate lets the hook URL resolve to private and loopback addresses (the
	// webhook_allow_private_urls setting).
	AllowPrivate bool
	// Client overrides the HTTP client (tests); by default an SSRF-safe client without
	// environment proxy that never follows redirects.
	Client *http.Client
	// Held reports an operator send suspension (cloud): the hook is not called then.
	Held func(instanceID string) bool
	// Concurrency bounds hook calls in flight overall (default 8).
	Concurrency int
	// BufferSize is how many events may wait for the router (default 1000).
	BufferSize int
	Log        *slog.Logger
	// Builtin answers for hooks with target "builtin" (the built-in attendant, spec 4.7);
	// nil: such hooks fail with reason not_configured.
	Builtin BuiltinAgent
}

// BuiltinAgent is the built-in attendant: it answers a request like an external hook
// would, without the HTTP round trip. Its errors are failures (fail-closed); a *CallError
// carries the agent.failed reason.
type BuiltinAgent interface {
	Answer(ctx context.Context, req Request) (Response, error)
}

// CallError is a built-in agent failure with a stable agent.failed reason
// (not_configured, daily_cap, llm_error, invalid_response...).
type CallError struct {
	Reason string
	Detail string
}

func (e *CallError) Error() string { return e.Reason + ": " + e.Detail }

// AgentEvent is an event the built-in attendant asks to publish once its answer is acted
// on (agent.lead, agent.appointment_requested, agent.order_draft).
type AgentEvent struct {
	Type string
	Data map[string]any
}

// builtinEventTypes are the only event types an AgentEvent may have.
var builtinEventTypes = map[string]bool{events.TypeAgentLead: true, events.TypeAgentAppointment: true,
	events.TypeAgentOrderDraft: true}

// Header names of a hook request (the signature is the webhook one, spec 5.3).
const (
	HeaderSignature = webhook.HeaderSignature
	HeaderEventID   = webhook.HeaderEventID
	HeaderEventType = webhook.HeaderEventType
	// RequestEventType is the X-LiteZap-Event-Type of a hook request.
	RequestEventType = "agent.request"
	maxResponseBytes = 8 << 20 // base64 media in a reply: prefer media.url
	maxActions       = 20
	maxNoteLen       = 1000
	maxDetailLen     = 500
)

// Hook is the events.Sink of the agent hook. Enqueue never blocks; a router goroutine
// applies human pauses in event order and hands messages to one goroutine per chat, so a
// chat never has two calls in flight. Messages that arrive during a call are coalesced:
// the next call carries the newest one (the others are in its context).
type Hook struct {
	st  *store.Store
	opt Options
	log *slog.Logger

	msgMu sync.RWMutex
	msg   Messenger
	pub   events.Publisher

	in      chan events.Envelope
	inMu    sync.RWMutex
	inShut  bool
	dropped atomic.Int64

	slots chan struct{}

	mu      sync.Mutex
	chats   map[chatKey]*chatState
	started bool

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup // chat goroutines
	routerD chan struct{}
}

type chatKey struct{ instance, chat string }

type chatState struct {
	pending *job // newest message waiting for the call in flight
}

type job struct {
	eventID string
	msg     events.Message
	raw     json.RawMessage // the message.received data, forwarded as is
	call    *callJob        // a call follow-up instead of a message (built-in attendant only)
}

// callJob is a rejected call the built-in attendant follows up on; fallback (the static
// calls.reject_message) is sent instead when the attendant fails.
type callJob struct {
	callID   string
	fallback string
}

// New returns a hook; set the messenger and publisher, then Start it.
func New(st *store.Store, opt Options) *Hook {
	if opt.Clock == nil {
		opt.Clock = clock.Real{}
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 8
	}
	if opt.BufferSize <= 0 {
		opt.BufferSize = 1000
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Client == nil {
		opt.Client = &http.Client{Transport: media.SafeTransport(opt.AllowPrivate)}
	}
	// A redirect is an answer, not a reply: never follow it (the signed request was meant
	// for this URL's owner).
	c := *opt.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	opt.Client = &c
	ctx, cancel := context.WithCancel(context.Background())
	return &Hook{st: st, opt: opt, log: opt.Log, in: make(chan events.Envelope, opt.BufferSize),
		slots: make(chan struct{}, opt.Concurrency), chats: map[chatKey]*chatState{},
		ctx: ctx, cancel: cancel, routerD: make(chan struct{})}
}

// SetMessenger sets the send path (before Start).
func (h *Hook) SetMessenger(m Messenger) { h.msgMu.Lock(); h.msg = m; h.msgMu.Unlock() }

// SetPublisher sets where agent.failed / agent.handoff go (before Start).
func (h *Hook) SetPublisher(p events.Publisher) { h.msgMu.Lock(); h.pub = p; h.msgMu.Unlock() }

func (h *Hook) messenger() Messenger {
	h.msgMu.RLock()
	defer h.msgMu.RUnlock()
	return h.msg
}

func (h *Hook) publish(instanceID, typ string, data any) {
	h.msgMu.RLock()
	p := h.pub
	h.msgMu.RUnlock()
	if p != nil {
		p.Publish(instanceID, typ, data)
	}
}

// Enqueue receives every bus event; only message.received and message.sent are kept.
func (h *Hook) Enqueue(e events.Envelope) {
	if e.Type != events.TypeMessageReceived && e.Type != events.TypeMessageSent {
		return
	}
	h.inMu.RLock()
	defer h.inMu.RUnlock()
	if h.inShut {
		return
	}
	select {
	case h.in <- e:
	default:
		h.dropped.Add(1)
		h.log.Error("agent hook: event buffer full; message not handled", "event_id", e.ID, "instance_id", e.InstanceID)
	}
}

// Dropped counts events lost to a full buffer.
func (h *Hook) Dropped() int64 { return h.dropped.Load() }

// Start runs the router.
func (h *Hook) Start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return
	}
	h.started = true
	go h.router()
}

// Close stops taking events and waits (up to ctx) for calls in flight; calls still
// running are cut and not reported (nothing is sent for them).
func (h *Hook) Close(ctx context.Context) {
	h.inMu.Lock()
	if !h.inShut {
		h.inShut = true
		close(h.in)
	}
	h.inMu.Unlock()
	h.mu.Lock()
	started := h.started
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		if started {
			<-h.routerD
		}
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		h.cancel()
		<-done
	}
	h.cancel()
}

type wireEnvelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	InstanceID string          `json:"instance_id"`
	Data       json.RawMessage `json:"data"`
}

func (h *Hook) router() {
	defer close(h.routerD)
	for e := range h.in {
		var w wireEnvelope
		if err := json.Unmarshal(e.JSON, &w); err != nil {
			h.log.Error("agent hook: decode event", "event_id", e.ID, "err", err)
			continue
		}
		var m events.Message
		if err := json.Unmarshal(w.Data, &m); err != nil || m.Chat == "" {
			continue
		}
		if skipChat(m.Chat) {
			continue
		}
		switch e.Type {
		case events.TypeMessageSent:
			switch m.Source {
			case "device": // a from_me message LiteZap did not send: a human answered
				h.humanReply(e.InstanceID, m, "human_reply")
			case "api": // a human through another API client (CRM, MCP), when configured
				h.apiSend(e.InstanceID, m)
			}
		case events.TypeMessageReceived:
			if !m.FromMe {
				h.dispatch(e.InstanceID, job{eventID: e.ID, msg: m, raw: w.Data})
			}
		}
	}
}

// skipChat reports chats the hook never handles: status updates and channels.
func skipChat(jid string) bool {
	return jid == "status@broadcast" || strings.HasSuffix(jid, "@broadcast") || strings.HasSuffix(jid, "@newsletter")
}

// humanReply pauses the hook in the chat for human_pause, drops a message waiting there
// and cancels the hook's replies still in the send queue (the bot never talks over a
// human).
func (h *Hook) humanReply(instanceID string, m events.Message, reason string) {
	ctx := h.ctx
	cfg, ok := h.config(ctx, instanceID)
	if !ok || cfg.HumanPause <= 0 {
		return
	}
	ca, err := h.chatAgent(ctx, instanceID, m.Chat)
	if err != nil {
		h.log.Error("agent hook: load chat", "instance_id", instanceID, "chat", m.Chat, "err", err)
		return
	}
	now := h.opt.Clock.Now()
	wasActive := active(cfg, ca, now)
	if err := h.st.PauseChatAgent(ctx, instanceID, m.Chat, now.Add(cfg.HumanPause.D())); err != nil {
		h.log.Error("agent hook: human pause", "instance_id", instanceID, "chat", m.Chat, "err", err)
		return
	}
	h.mu.Lock()
	if cs := h.chats[chatKey{instanceID, m.Chat}]; cs != nil {
		cs.pending = nil
	}
	h.mu.Unlock()
	canceled := h.cancelQueued(instanceID, m.Chat, "a human answered in the chat")
	if wasActive || canceled > 0 { // the log shows pauses that changed something
		detail := "paused for " + cfg.HumanPause.D().String()
		if canceled > 0 {
			detail += "; " + itoa(canceled) + " queued agent repl(ies) canceled"
		}
		h.record(store.AgentCall{InstanceID: instanceID, ChatJID: m.Chat, MessageID: m.ID, Status: "paused",
			Reason: reason, Detail: detail})
	}
}

// apiSend treats an API send that is not the hook's own reply as a human reply when
// pause_on_api_send is on (an inbox or CRM on top of LiteZap).
func (h *Hook) apiSend(instanceID string, m events.Message) {
	cfg, ok := h.config(h.ctx, instanceID)
	if !ok || !cfg.PauseOnAPISend {
		return
	}
	o, err := h.st.GetOutbound(h.ctx, instanceID, m.ID)
	if err != nil || strings.HasPrefix(o.ClientMessageID, "agent:") || strings.HasPrefix(o.ClientMessageID, "agent-react:") {
		return
	}
	h.humanReply(instanceID, m, "api_send")
}

// cancelQueued cancels the hook's queued replies in a chat and returns how many.
func (h *Hook) cancelQueued(instanceID, chat, reason string) int {
	msgr := h.messenger()
	if msgr == nil {
		return 0
	}
	n, err := msgr.CancelAgentReplies(h.ctx, instanceID, chat, reason)
	if err != nil {
		h.log.Error("agent hook: cancel queued replies", "instance_id", instanceID, "chat", chat, "err", err)
	}
	return n
}

// dispatch runs the job on the chat's goroutine, starting one if needed; a job arriving
// while a call is in flight replaces the waiting one.
func (h *Hook) dispatch(instanceID string, j job) {
	k := chatKey{instanceID, j.msg.Chat}
	h.mu.Lock()
	if cs, ok := h.chats[k]; ok {
		// A call follow-up never displaces a message waiting for its turn.
		if j.call == nil || cs.pending == nil || cs.pending.call != nil {
			cs.pending = &j
		}
		h.mu.Unlock()
		return
	}
	h.chats[k] = &chatState{}
	h.wg.Add(1)
	h.mu.Unlock()
	go h.chatLoop(k, j)
}

func (h *Hook) chatLoop(k chatKey, j job) {
	defer h.wg.Done()
	for {
		h.handle(k.instance, j)
		h.mu.Lock()
		cs := h.chats[k]
		if cs.pending == nil || h.ctx.Err() != nil {
			delete(h.chats, k)
			h.mu.Unlock()
			return
		}
		j = *cs.pending
		cs.pending = nil
		h.mu.Unlock()
	}
}

// config loads the instance's hook config; ok is false when there is none (or no URL).
func (h *Hook) config(ctx context.Context, instanceID string) (Config, bool) {
	js, err := h.st.GetAgentHook(ctx, instanceID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.log.Error("agent hook: load config", "instance_id", instanceID, "err", err)
		}
		return Config{}, false
	}
	cfg, err := parseConfig(js)
	if err != nil {
		h.log.Error("agent hook: bad stored config", "instance_id", instanceID, "err", err)
		return Config{}, false
	}
	return cfg, cfg.URL != "" || cfg.Builtin()
}

// chatAgent loads the chat's override and pause (zero state for an unknown chat).
func (h *Hook) chatAgent(ctx context.Context, instanceID, chat string) (store.ChatAgent, error) {
	ca, err := h.st.GetChatAgent(ctx, instanceID, chat)
	if errors.Is(err, store.ErrNotFound) {
		return store.ChatAgent{JID: chat}, nil
	}
	return ca, err
}

// active reports whether the hook handles the chat now (override, else the instance
// default; not paused).
func active(cfg Config, ca store.ChatAgent, now time.Time) bool {
	on := cfg.Enabled
	if ca.Enabled != nil {
		on = *ca.Enabled
	}
	return on && !ca.PausedUntil.After(now)
}

// handle runs the hook for one message (spec 4.3).
func (h *Hook) handle(instanceID string, j job) {
	ctx := h.ctx
	cfg, ok := h.config(ctx, instanceID)
	if !ok {
		return
	}
	m := j.msg
	ca, err := h.chatAgent(ctx, instanceID, m.Chat)
	if err != nil {
		h.log.Error("agent hook: load chat", "instance_id", instanceID, "chat", m.Chat, "err", err)
		return
	}
	now := h.opt.Clock.Now()
	isGroup := m.IsGroup || ca.IsGroup || strings.HasSuffix(m.Chat, "@g.us")
	switch {
	case isGroup && !cfg.GroupsEnabled, cfg.ignores(m.Chat, m.Sender), !active(cfg, ca, now), m.Type == "unknown":
		return
	}
	base := store.AgentCall{InstanceID: instanceID, ChatJID: m.Chat, MessageID: m.ID}
	if h.stale(cfg, m, now) {
		base.Status, base.Reason, base.Detail = "skipped", "stale", "message received "+now.Sub(m.Timestamp).Round(time.Second).String()+" ago (offline backlog)"
		h.record(base)
		return
	}
	if h.opt.Held != nil && h.opt.Held(instanceID) {
		base.Status, base.Reason, base.Detail = "skipped", "sending_suspended", "sending is suspended by the operator"
		h.record(base)
		return
	}
	// Loop guard: too many replies in the window means two bots talking, or a runaway agent.
	if n, err := h.st.CountAgentReplies(ctx, instanceID, m.Chat, now.Add(-cfg.ReplyWindow.D())); err != nil {
		h.log.Error("agent hook: count replies", "instance_id", instanceID, "err", err)
		return
	} else if n >= cfg.MaxRepliesPerWindow {
		h.loopGuard(ctx, cfg, base, j, n, now)
		return
	}

	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	if h.stale(cfg, m, h.opt.Clock.Now()) { // waited too long for a slot
		<-h.slots
		base.Status, base.Reason, base.Detail = "skipped", "stale", "waited too long for a free call slot"
		h.record(base)
		return
	}
	res := h.call(ctx, cfg, instanceID, ca, j)
	<-h.slots
	if ctx.Err() != nil {
		return // shutting down: not reported, nothing sent
	}
	base.HTTPStatus, base.DurationMS = res.status, res.duration.Milliseconds()
	if res.reason != "" {
		h.fail(base, j, res.reason, res.err, res.status)
		if j.call != nil {
			h.callFallback(instanceID, j)
		}
		return
	}
	h.act(ctx, cfg, base, j, res.resp)
}

// staleAfter is how old a message may be when its call starts: older ones are an offline
// backlog delivered after a reconnection, and a reply hours late does more harm than good.
func staleAfter(cfg Config) time.Duration { return max(2*cfg.Timeout.D(), 5*time.Minute) }

func (h *Hook) stale(cfg Config, m events.Message, now time.Time) bool {
	return !m.Timestamp.IsZero() && now.Sub(m.Timestamp) > staleAfter(cfg)
}

// loopGuard pauses a chat that got too many replies; a second trip within 24h hands the
// chat over to a human for good (two bots would otherwise resume after every pause).
func (h *Hook) loopGuard(ctx context.Context, cfg Config, base store.AgentCall, j job, n int, now time.Time) {
	m := j.msg
	trips, err := h.st.CountAgentCalls(ctx, base.InstanceID, m.Chat, "failed", "loop_guard", now.Add(-store.AgentCallsMinAge))
	if err != nil {
		h.log.Error("agent hook: count loop guard trips", "instance_id", base.InstanceID, "err", err)
	}
	var msg string
	if trips > 0 {
		off := false
		if err := h.st.SetChatAgent(ctx, base.InstanceID, m.Chat, &off, false); err != nil {
			h.log.Error("agent hook: loop guard handoff", "instance_id", base.InstanceID, "err", err)
		}
		msg = fmt.Sprintf("%d replies in %s again within 24h; hook turned off in this chat (handoff)", n, cfg.ReplyWindow.D())
		h.publish(base.InstanceID, events.TypeAgentHandoff, events.AgentHandoff{Chat: m.Chat, MessageID: m.ID,
			WAMessageID: m.WAMessageID, Note: "loop_guard: too many automatic replies; check this chat"})
	} else {
		pause := cfg.HumanPause.D()
		if pause <= 0 {
			pause = cfg.ReplyWindow.D()
		}
		if err := h.st.PauseChatAgent(ctx, base.InstanceID, m.Chat, now.Add(pause)); err != nil {
			h.log.Error("agent hook: loop guard pause", "instance_id", base.InstanceID, "err", err)
		}
		msg = fmt.Sprintf("%d replies in %s; hook paused in this chat for %s", n, cfg.ReplyWindow.D(), pause)
	}
	h.cancelQueued(base.InstanceID, m.Chat, "agent hook loop guard")
	h.fail(base, j, "loop_guard", msg, 0)
}

// fail logs a failed call and publishes agent.failed.
func (h *Hook) fail(c store.AgentCall, j job, reason, detail string, status int) {
	c.Status, c.Reason, c.Detail = "failed", reason, detail
	h.record(c)
	h.publish(c.InstanceID, events.TypeAgentFailed, events.AgentFailed{Chat: j.msg.Chat, MessageID: j.msg.ID,
		WAMessageID: j.msg.WAMessageID, Reason: reason, Error: truncate(detail, maxDetailLen), HTTPStatus: status})
}

func (h *Hook) record(c store.AgentCall) {
	c.ID = secure.NewID("agc_")
	c.CreatedAt = h.opt.Clock.Now()
	c.Detail = truncate(c.Detail, maxDetailLen)
	if err := h.st.InsertAgentCall(context.Background(), c); err != nil {
		h.log.Error("agent hook: log call", "instance_id", c.InstanceID, "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Request is the body POSTed to the hook (spec 4.3).
type Request struct {
	InstanceID string           `json:"instance_id"`
	Chat       ChatInfo         `json:"chat"`
	Message    json.RawMessage  `json:"message"` // the data of message.received
	Context    []ContextMessage `json:"context"` // last N messages, oldest first (ends with this one)
	// Trigger is "call" for a call follow-up (built-in attendant only; Message then
	// describes the call); empty for a received message.
	Trigger string `json:"trigger,omitempty"`
}

type ChatInfo struct {
	JID     string `json:"jid"`
	Name    string `json:"name,omitempty"`
	IsGroup bool   `json:"is_group"`
}

// ContextMessage is a logged message as the hook sees it.
type ContextMessage struct {
	ID          string `json:"id"`
	WAMessageID string `json:"wa_message_id,omitempty"`
	Sender      string `json:"sender,omitempty"`
	FromMe      bool   `json:"from_me"`
	Source      string `json:"source"` // contact | api | device | history
	// Origin says who wrote it: contact (the customer or a group participant), agent (the
	// agent's own replies), human (someone answering from the phone), automatic (automatic
	// messages such as the call reject message), api (other API sends: CRM, inbox,
	// scripts) or history (synced from the phone; author unknown).
	Origin    string        `json:"origin"`
	Type      string        `json:"type"`
	Text      string        `json:"text,omitempty"`
	Media     *events.Media `json:"media,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
	Revoked   bool          `json:"revoked,omitempty"`
}

// Response is the hook's answer.
type Response struct {
	Reply   json.RawMessage   `json:"reply"`
	Actions []json.RawMessage `json:"actions"`
	// Events are published after the answer is acted on (built-in attendant only; never
	// read from an external hook's JSON).
	Events []AgentEvent `json:"-"`
}

type callResult struct {
	resp     Response
	status   int
	duration time.Duration
	reason   string // failure code ("" = success)
	err      string
}

func (h *Hook) context(ctx context.Context, instanceID, chat string, n int) ([]ContextMessage, error) {
	out := []ContextMessage{}
	if n == 0 {
		return out, nil
	}
	msgs, _, err := h.st.ListMessages(ctx, instanceID, chat, n, nil)
	if err != nil {
		return nil, err
	}
	var apiIDs []string
	for _, m := range msgs {
		if m.FromMe && m.Source == "api" {
			apiIDs = append(apiIDs, m.ID)
		}
	}
	clientIDs, err := h.st.ClientIDsForMessages(ctx, instanceID, apiIDs)
	if err != nil {
		return nil, err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		cm := ContextMessage{ID: m.ID, WAMessageID: m.WAMessageID, Sender: m.SenderJID, FromMe: m.FromMe,
			Source: m.Source, Origin: Origin(m.FromMe, m.Source, clientIDs[m.ID]), Type: m.Type, Text: m.Text,
			Timestamp: m.Timestamp, Revoked: !m.RevokedAt.IsZero()}
		if m.MediaJSON != "" {
			var mi store.MediaInfo
			if json.Unmarshal([]byte(m.MediaJSON), &mi) == nil {
				cm.Media = &events.Media{ID: mi.ID, Kind: mi.Kind, Mime: mi.Mime, Size: mi.Size, FileName: mi.FileName,
					Seconds: mi.Seconds, PTT: mi.PTT}
				if mi.ID != "" {
					cm.Media.URL = "/v1/instances/" + instanceID + "/media/" + mi.ID
				}
			}
		}
		out = append(out, cm)
	}
	return out, nil
}

// Origin classifies a logged message for the agent's context (ContextMessage.Origin);
// clientID is the client_message_id of an API send ("" when unknown).
func Origin(fromMe bool, source, clientID string) string {
	switch {
	case !fromMe:
		return "contact"
	case source == "device":
		return "human"
	case source == "history":
		return "history"
	case strings.HasPrefix(clientID, "agent:") || strings.HasPrefix(clientID, "agent-react:"):
		return "agent"
	case strings.HasPrefix(clientID, "call-reject-"):
		return "automatic"
	default:
		return "api"
	}
}

// call POSTs the signed request (or asks the built-in attendant) and reads the response;
// it never retries.
func (h *Hook) call(ctx context.Context, cfg Config, instanceID string, ca store.ChatAgent, j job) callResult {
	cms, err := h.context(ctx, instanceID, j.msg.Chat, cfg.ContextMessages)
	if err != nil {
		return callResult{reason: "internal", err: "load context: " + err.Error()}
	}
	hreq := Request{InstanceID: instanceID,
		Chat:    ChatInfo{JID: j.msg.Chat, Name: ca.Name, IsGroup: j.msg.IsGroup || ca.IsGroup},
		Message: j.raw, Context: cms}
	if cfg.Builtin() {
		return h.callBuiltin(ctx, cfg, hreq, j)
	}
	body, err := json.Marshal(hreq)
	if err != nil {
		return callResult{reason: "internal", err: err.Error()}
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout.D())
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return callResult{reason: "network_error", err: "invalid url"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LiteZap-Agent/1")
	req.Header.Set(HeaderEventID, j.eventID)
	req.Header.Set(HeaderEventType, RequestEventType)
	req.Header.Set(HeaderSignature, webhook.Sign(cfg.Secret, h.opt.Clock.Now(), body))
	start := time.Now()
	resp, err := h.opt.Client.Do(req)
	if err != nil {
		r := callResult{duration: time.Since(start), reason: "network_error", err: cleanErr(err).Error()}
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			r.reason, r.err = "timeout", "no response within "+cfg.Timeout.D().String()
		}
		return r
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	r := callResult{status: resp.StatusCode, duration: time.Since(start)}
	switch {
	case err != nil:
		r.reason, r.err = "network_error", "read response: "+cleanErr(err).Error()
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			r.reason, r.err = "timeout", "no response within "+cfg.Timeout.D().String()
		}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		r.reason, r.err = "http_error", "HTTP "+itoa(resp.StatusCode)
	case len(raw) > maxResponseBytes:
		r.reason, r.err = "invalid_response", "response larger than 8MB"
	case resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(raw)) == 0:
	default:
		if err := json.Unmarshal(raw, &r.resp); err != nil {
			r.reason, r.err = "invalid_response", "response is not a JSON object: "+err.Error()
		}
	}
	return r
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// cleanErr drops the URL from client errors (it may carry credentials in its query).
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// action is one entry of "actions".
type action struct {
	Type    string `json:"type"`
	Emoji   string `json:"emoji"`
	Minutes *int   `json:"minutes"`
	Note    string `json:"note"`
}

// replyTypes are the message types a reply may have (no edit/revoke/pin/forward: a reply
// only adds content to the chat that triggered it; reactions are the react action).
var replyTypes = map[string]bool{"text": true, "image": true, "video": true, "audio": true, "document": true,
	"sticker": true, "gif": true, "ptv": true, "location": true, "contact": true, "poll": true}

// plan validates the whole response before anything is done (fail-closed).
func plan(resp Response, chat, messageID string) (*service.SendInput, []action, []string, error) {
	var reply *service.SendInput
	if r := bytes.TrimSpace(resp.Reply); len(r) > 0 && !bytes.Equal(r, []byte("null")) {
		switch r[0] {
		case '"':
			var s string
			if err := json.Unmarshal(r, &s); err != nil {
				return nil, nil, nil, errors.New("reply is not a valid string")
			}
			if strings.TrimSpace(s) != "" {
				reply = &service.SendInput{Type: "text", Text: s}
			}
		case '{':
			var in service.SendInput
			if err := json.Unmarshal(r, &in); err != nil {
				return nil, nil, nil, errors.New("reply is not a valid message payload: " + err.Error())
			}
			if in.Type == "" {
				in.Type = "text"
			}
			if !replyTypes[in.Type] {
				return nil, nil, nil, fmt.Errorf("reply type %q is not allowed", in.Type)
			}
			for _, mt := range in.Mentions {
				if mt == "all" {
					return nil, nil, nil, errors.New(`mentions ["all"] is not allowed in agent replies`)
				}
			}
			reply = &in
		default:
			return nil, nil, nil, errors.New("reply must be a string or a message object")
		}
	}
	if reply != nil {
		// Always to the chat that triggered the call, once per message (idempotent).
		reply.To, reply.ClientMessageID = chat, "agent:"+messageID
		reply.ExpiresAfter = "1h" // a reply hours late is worse than none
	}
	if len(resp.Actions) > maxActions {
		return nil, nil, nil, fmt.Errorf("at most %d actions", maxActions)
	}
	var acts []action
	var unknown []string
	for _, ra := range resp.Actions {
		var a action
		if err := json.Unmarshal(ra, &a); err != nil {
			return nil, nil, nil, errors.New("action is not an object")
		}
		switch a.Type {
		case "mark_read":
		case "react":
			if a.Emoji == "" || len(a.Emoji) > 64 {
				return nil, nil, nil, errors.New("react needs an emoji")
			}
		case "pause":
			if a.Minutes == nil || *a.Minutes < 1 || *a.Minutes > 7*24*60 {
				return nil, nil, nil, errors.New("pause needs minutes between 1 and 10080")
			}
		case "handoff":
			a.Note = truncate(a.Note, maxNoteLen)
		default:
			unknown = append(unknown, a.Type)
			continue
		}
		acts = append(acts, a)
	}
	return reply, acts, unknown, nil
}

// act executes a successful response: the reply first (it is validated by the send path,
// so a rejected reply leaves nothing else sent), then mark_read and react, then
// pause/handoff, which always apply (they only make the hook safer). A human taking over
// during the call discards everything.
func (h *Hook) act(ctx context.Context, cfg Config, c store.AgentCall, j job, resp Response) {
	m := j.msg
	reply, acts, unknown, err := plan(resp, m.Chat, m.ID)
	if err != nil {
		h.fail(c, j, "invalid_response", err.Error(), c.HTTPStatus)
		return
	}
	if len(unknown) > 0 {
		h.log.Warn("agent hook: unknown actions ignored", "instance_id", c.InstanceID, "types", unknown)
	}
	if reply == nil && len(acts) == 0 {
		c.Status = "ok"
		h.record(c)
		h.publishEvents(c.InstanceID, m, resp.Events)
		return
	}
	ca, err := h.chatAgent(ctx, c.InstanceID, m.Chat)
	if err != nil {
		h.fail(c, j, "internal", "reload chat: "+err.Error(), c.HTTPStatus)
		return
	}
	if !active(cfg, ca, h.opt.Clock.Now()) { // a human answered, or the chat was paused/disabled
		c.Status, c.Reason, c.Detail = "discarded", "chat_paused", "the chat was paused or disabled during the call"
		h.record(c)
		return
	}
	msgr := h.messenger()
	if msgr == nil {
		h.fail(c, j, "internal", "no send path", c.HTTPStatus)
		return
	}
	var done, notes []string
	failReason, failErr := "", ""
	if reply != nil {
		v, err := msgr.Send(ctx, c.InstanceID, *reply, 0)
		if err != nil {
			failReason, failErr = "send_failed", err.Error()
			var ae *apierr.Error
			if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
				failReason = "invalid_response" // the reply payload itself is invalid
			}
		} else {
			c.Replied, c.ReplyID = true, v.ID
		}
	}
	if failReason == "" {
		for _, a := range acts {
			switch a.Type {
			case "mark_read":
				if err := msgr.MarkRead(ctx, c.InstanceID, m.Chat); err != nil {
					notes = append(notes, "mark_read: "+err.Error())
					continue
				}
			case "react":
				if _, err := msgr.Send(ctx, c.InstanceID, service.SendInput{To: m.Chat, Type: "reaction",
					Reaction: &service.ReactionInput{MessageID: m.ID, Emoji: a.Emoji}, ClientMessageID: "agent-react:" + m.ID,
					ExpiresAfter: "1h"}, 0); err != nil {
					notes = append(notes, "react: "+err.Error())
					continue
				}
			default:
				continue
			}
			done = append(done, a.Type)
		}
	}
	now := h.opt.Clock.Now()
	for _, a := range acts {
		switch a.Type {
		case "pause":
			if err := h.st.PauseChatAgent(ctx, c.InstanceID, m.Chat, now.Add(time.Duration(*a.Minutes)*time.Minute)); err != nil {
				notes = append(notes, "pause: "+err.Error())
				continue
			}
		case "handoff":
			off := false
			if err := h.st.SetChatAgent(ctx, c.InstanceID, m.Chat, &off, false); err != nil {
				notes = append(notes, "handoff: "+err.Error())
				continue
			}
			h.publish(c.InstanceID, events.TypeAgentHandoff, events.AgentHandoff{Chat: m.Chat, MessageID: m.ID,
				WAMessageID: m.WAMessageID, Note: a.Note})
		default:
			continue
		}
		done = append(done, a.Type)
	}
	c.Actions = strings.Join(done, ",")
	if failReason != "" {
		h.fail(c, j, failReason, failErr, c.HTTPStatus)
		return
	}
	c.Status, c.Detail = "ok", strings.Join(notes, "; ")
	h.record(c)
	h.publishEvents(c.InstanceID, m, resp.Events)
}

// publishEvents publishes the built-in attendant's events (known types only), with the
// chat and the triggering message filled in by the engine.
func (h *Hook) publishEvents(instanceID string, m events.Message, evs []AgentEvent) {
	for _, e := range evs {
		if !builtinEventTypes[e.Type] {
			continue
		}
		data := map[string]any{}
		for k, v := range e.Data {
			data[k] = v
		}
		data["chat"] = m.Chat
		delete(data, "message_id")
		delete(data, "wa_message_id")
		if !strings.HasPrefix(m.ID, "call:") {
			data["message_id"] = m.ID
			if m.WAMessageID != "" {
				data["wa_message_id"] = m.WAMessageID
			}
		}
		h.publish(instanceID, e.Type, data)
	}
}
