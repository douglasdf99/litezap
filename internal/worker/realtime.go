package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Realtime is implemented by WAClients that handle calls and presence (marco 7). It is
// separate from Messenger so other clients keep compiling; without it the RPCs answer
// Unimplemented.
type Realtime interface {
	// RejectCall rejects a call the client reported (NotFound for unknown ids).
	RejectCall(ctx context.Context, callID string) error
	// SetPresence marks the account available (online) or unavailable.
	SetPresence(ctx context.Context, available bool) error
	// SubscribePresence asks for a contact's presence updates.
	SubscribePresence(ctx context.Context, jid string) error
}

var _ Realtime = (*Whatsmeow)(nil)

func (s *Server) realtime() (Realtime, error) {
	rt, ok := s.wa.(Realtime)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "calls and presence not supported by this worker")
	}
	return rt, nil
}

func (s *Server) RejectCall(ctx context.Context, req *workerv1.RejectCallRequest) (*workerv1.RejectCallResponse, error) {
	rt, err := s.realtime()
	if err != nil {
		return nil, err
	}
	if err := rt.RejectCall(ctx, req.CallId); err != nil {
		return nil, err
	}
	return &workerv1.RejectCallResponse{}, nil
}

func (s *Server) SetPresence(ctx context.Context, req *workerv1.SetPresenceRequest) (*workerv1.SetPresenceResponse, error) {
	rt, err := s.realtime()
	if err != nil {
		return nil, err
	}
	if err := rt.SetPresence(ctx, req.Available); err != nil {
		return nil, err
	}
	return &workerv1.SetPresenceResponse{}, nil
}

func (s *Server) SubscribePresence(ctx context.Context, req *workerv1.SubscribePresenceRequest) (*workerv1.SubscribePresenceResponse, error) {
	rt, err := s.realtime()
	if err != nil {
		return nil, err
	}
	if err := rt.SubscribePresence(ctx, req.Jid); err != nil {
		return nil, err
	}
	return &workerv1.SubscribePresenceResponse{}, nil
}

// Calls kept for RejectCall: offers are forgotten when they end, after callTTL, or
// (oldest first) beyond maxCalls.
const (
	maxCalls = 256
	callTTL  = 10 * time.Minute
)

type callEntry struct {
	from types.JID // the call creator, as RejectCall wants it
	seen time.Time
}

// callBook remembers the calls a worker reported, so RejectCall never takes a raw JID.
type callBook struct {
	mu    sync.Mutex
	calls map[string]callEntry
}

func (b *callBook) add(id string, from types.JID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls == nil {
		b.calls = map[string]callEntry{}
	}
	for k, c := range b.calls {
		if now.Sub(c.seen) > callTTL {
			delete(b.calls, k)
		}
	}
	for len(b.calls) >= maxCalls {
		oldest, first := "", true
		var at time.Time
		for k, c := range b.calls {
			if first || c.seen.Before(at) {
				oldest, at, first = k, c.seen, false
			}
		}
		delete(b.calls, oldest)
	}
	b.calls[id] = callEntry{from: from, seen: now}
}

func (b *callBook) get(id string, now time.Time) (types.JID, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.calls[id]
	if !ok || now.Sub(c.seen) > callTTL {
		return types.EmptyJID, false
	}
	return c.from, true
}

func (b *callBook) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.calls, id)
}

// callMedia tells audio from video by the offer's children (<video> is present on video calls).
func callMedia(e *events.CallOffer) string {
	if e.Data == nil {
		return ""
	}
	for _, c := range e.Data.GetChildren() {
		if c.Tag == "video" {
			return "video"
		}
	}
	return "audio"
}

// toCall normalizes the call events we report; nil for the others (relay latency,
// transport, pre-accept: internal signalling).
func (w *Whatsmeow) toCall(evt any) (*workerv1.Call, types.BasicCallMeta, bool) {
	var c workerv1.Call
	var meta types.BasicCallMeta
	switch e := evt.(type) {
	case *events.CallOffer:
		c.Kind, c.Media, meta = "offer", callMedia(e), e.BasicCallMeta
	case *events.CallOfferNotice:
		c.Kind, c.Media, meta = "offer_notice", e.Media, e.BasicCallMeta
	case *events.CallAccept:
		c.Kind, meta = "accept", e.BasicCallMeta
	case *events.CallTerminate:
		c.Kind, c.Reason, meta = "terminate", e.Reason, e.BasicCallMeta
	case *events.CallReject:
		c.Kind, meta = "reject", e.BasicCallMeta
	default:
		return nil, meta, false
	}
	if meta.CallID == "" {
		return nil, meta, false
	}
	creator := meta.CallCreator
	if creator.IsEmpty() {
		creator = meta.From
	}
	c.CallId = meta.CallID
	c.Caller = w.phoneJID(creator, meta.CallCreatorAlt, nil).String()
	if !meta.GroupJID.IsEmpty() {
		c.Group = meta.GroupJID.ToNonAD().String()
	}
	c.TimestampMs = meta.Timestamp.UnixMilli()
	if meta.Timestamp.IsZero() {
		c.TimestampMs = time.Now().UnixMilli()
	}
	return &c, meta, true
}

// onRealtime handles call and presence events; it reports whether evt was one.
func (w *Whatsmeow) onRealtime(evt any) bool {
	switch e := evt.(type) {
	case *events.ChatPresence:
		if outOfScope(types.MessageInfo{MessageSource: e.MessageSource}) || e.IsFromMe {
			return true
		}
		state := "paused"
		if e.State == types.ChatPresenceComposing {
			state = "composing"
			if e.Media == types.ChatPresenceMediaAudio {
				state = "recording"
			}
		}
		chat, sender := w.addressing(types.MessageInfo{MessageSource: e.MessageSource}, nil)
		w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_ChatPresence{ChatPresence: &workerv1.ChatPresence{
			Chat: chat.String(), Sender: sender.String(), IsGroup: e.IsGroup, State: state,
		}}})
		return true
	case *events.Presence:
		if e.From.IsEmpty() || e.From.Server == types.GroupServer {
			return true
		}
		p := &workerv1.Presence{Jid: w.phoneJID(e.From, types.EmptyJID, nil).String(), Online: !e.Unavailable}
		if !e.LastSeen.IsZero() {
			p.LastSeenMs = e.LastSeen.UnixMilli()
		}
		w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Presence{Presence: p}})
		return true
	case *events.CallRelayLatency, *events.CallPreAccept, *events.CallTransport, *events.UnknownCallEvent:
		return true
	}
	c, meta, ok := w.toCall(evt)
	if !ok {
		return false
	}
	switch c.Kind {
	case "offer", "offer_notice":
		// RejectCall puts this JID in both "to" and "call-creator": the creator, as the
		// offer named it (falling back to the stanza's sender).
		creator := meta.CallCreator
		if creator.IsEmpty() {
			creator = meta.From
		}
		w.calls.add(c.CallId, creator, time.Now())
	case "terminate", "reject":
		w.calls.remove(c.CallId)
	}
	w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_Call{Call: c}})
	return true
}

func (w *Whatsmeow) RejectCall(ctx context.Context, callID string) error {
	from, ok := w.calls.get(callID, time.Now())
	if !ok {
		return grpcErr(codes.NotFound, "not_found", "unknown or finished call")
	}
	reject := w.rejectCall
	if reject == nil {
		reject = w.cli.RejectCall
	}
	if err := reject(ctx, from, callID); err != nil {
		return sendErr(err)
	}
	w.calls.remove(callID)
	return nil
}

func (w *Whatsmeow) SetPresence(ctx context.Context, available bool) error {
	state := types.PresenceUnavailable
	if available {
		state = types.PresenceAvailable
	}
	set := w.sendPresence
	if set == nil {
		set = w.cli.SendPresence
	}
	if err := set(ctx, state); err != nil {
		if errors.Is(err, whatsmeow.ErrNoPushName) {
			// The push name arrives with the first app state sync after pairing.
			return grpcErr(codes.Unavailable, "instance_not_connected", "profile name not synced yet; try again shortly")
		}
		return sendErr(err)
	}
	return nil
}

func (w *Whatsmeow) SubscribePresence(ctx context.Context, raw string) error {
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
		return grpcErr(codes.InvalidArgument, "validation_failed", "presence needs a contact JID")
	}
	sub := w.subscribePresence
	if sub == nil {
		sub = w.cli.SubscribePresence
	}
	if err := sub(ctx, jid.ToNonAD()); err != nil {
		return sendErr(err)
	}
	return nil
}
