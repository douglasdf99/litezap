package agenthook

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
)

// callBuiltin asks the built-in attendant (target "builtin") instead of POSTing; the rest
// of the pipeline (validation of the answer, revalidation, the send queue) is the same.
func (h *Hook) callBuiltin(ctx context.Context, cfg Config, req Request, j job) callResult {
	if h.opt.Builtin == nil {
		return callResult{reason: "not_configured", err: "the built-in attendant is not available on this server"}
	}
	if j.call != nil {
		req.Trigger = "call"
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout.D())
	defer cancel()
	start := time.Now()
	resp, err := h.opt.Builtin.Answer(cctx, req)
	r := callResult{duration: time.Since(start), resp: resp}
	if err != nil {
		var ce *CallError
		switch {
		case errors.As(err, &ce):
			r.reason, r.err = ce.Reason, ce.Detail
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			r.reason, r.err = "timeout", "no answer within "+cfg.Timeout.D().String()
		default:
			r.reason, r.err = "internal", err.Error()
		}
		r.resp = Response{}
	}
	return r
}

// CallFollowUp is asked by the call handler (after its per-caller cooldown) when a 1:1
// call was rejected automatically: when the instance's hook uses the built-in attendant
// and it is active in the caller's chat, the attendant writes a short message there
// ("vi que você ligou...") through the normal pipeline and true is returned; fallback is
// sent instead if the attendant fails. false: the caller sends its static message.
func (h *Hook) CallFollowUp(instanceID, caller, callID, fallback string) bool {
	if h.opt.Builtin == nil || caller == "" || callID == "" || skipChat(caller) || strings.HasSuffix(caller, "@g.us") {
		return false
	}
	h.inMu.RLock()
	shut := h.inShut
	h.inMu.RUnlock()
	if shut {
		return false
	}
	ctx := h.ctx
	cfg, ok := h.config(ctx, instanceID)
	if !ok || !cfg.Builtin() || cfg.ignores(caller) {
		return false
	}
	ca, err := h.chatAgent(ctx, instanceID, caller)
	if err != nil || ca.IsGroup || !active(cfg, ca, h.opt.Clock.Now()) {
		return false
	}
	if h.opt.Held != nil && h.opt.Held(instanceID) {
		return false
	}
	now := h.opt.Clock.Now()
	raw, _ := json.Marshal(map[string]any{"type": "call", "chat": caller, "call_id": callID, "timestamp": now})
	h.dispatch(instanceID, job{eventID: "call:" + callID, raw: raw, call: &callJob{callID: callID, fallback: fallback},
		msg: events.Message{ID: "call:" + callID, Chat: caller, Type: "call", Timestamp: now}})
	return true
}

// callFallback sends the static reject message after a failed call follow-up (the same
// client id as the call handler's own, so a replay never sends it twice).
func (h *Hook) callFallback(instanceID string, j job) {
	if j.call == nil || strings.TrimSpace(j.call.fallback) == "" {
		return
	}
	msgr := h.messenger()
	if msgr == nil {
		return
	}
	if _, err := msgr.Send(h.ctx, instanceID, service.SendInput{To: j.msg.Chat, Type: "text", Text: j.call.fallback,
		ClientMessageID: "call-reject-" + j.call.callID, ExpiresAfter: "1h"}, 0); err != nil {
		h.log.Warn("agent hook: queue call reject message", "instance_id", instanceID, "err", err)
	}
}
