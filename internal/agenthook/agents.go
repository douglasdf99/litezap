package agenthook

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/store"
)

// AgentsRunner runs the LiteZap agents (target "agents"): unlike the attendant, an agent
// acts during its run (tool calls go through the guarded send path themselves), so the
// hook only provides the trigger and its guards: one run at a time per chat with
// coalescing, ignored and paused chats, stale messages, the operator's suspension and the
// loop guard. Guard must be called before every write: it reports whether the chat is
// still the agents' (a human took over, the chat was paused or turned off).
type AgentsRunner interface {
	RunForMessage(ctx context.Context, req Request, guard func(context.Context) error) (AgentsOutcome, error)
}

// AgentsOutcome sums up what a run did for the call log and the loop guard.
type AgentsOutcome struct {
	// NoMatch: no enabled agent's trigger matched the message (nothing is logged).
	NoMatch bool
	// Replied: something was sent in the trigger chat (counts in the loop guard).
	Replied bool
	ReplyID string
	// Detail is a short summary for the call log (agent, run id, status).
	Detail string
	// Actions lists the write tools the run executed or drafted.
	Actions []string
}

// ErrChatTaken is returned by a guard when the chat is no longer the agents'.
var ErrChatTaken = errors.New("a human took over the chat, or it was paused or turned off")

// agentsTimeout bounds one agents run (several model calls and tool calls).
const agentsTimeout = 3 * time.Minute

func (h *Hook) callAgents(ctx context.Context, cfg Config, req Request, j job) callResult {
	if h.opt.Agents == nil {
		return callResult{reason: "not_configured", err: "LiteZap agents are not available on this server"}
	}
	if j.call != nil { // call follow-ups are the attendant's
		return callResult{agents: &AgentsOutcome{NoMatch: true}}
	}
	cctx, cancel := context.WithTimeout(ctx, max(cfg.Timeout.D(), agentsTimeout))
	defer cancel()
	start := time.Now()
	guard := func(gctx context.Context) error { return h.stillActive(gctx, req.InstanceID, req.Chat.JID) }
	out, err := h.opt.Agents.RunForMessage(cctx, req, guard)
	r := callResult{duration: time.Since(start)}
	if err != nil {
		var ce *CallError
		switch {
		case errors.As(err, &ce):
			r.reason, r.err = ce.Reason, ce.Detail
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			r.reason, r.err = "timeout", "the agent did not finish within "+max(cfg.Timeout.D(), agentsTimeout).String()
		default:
			r.reason, r.err = "internal", err.Error()
		}
		return r
	}
	r.agents = &out
	return r
}

// stillActive re-reads the chat state: nil while the agents may still act in it.
func (h *Hook) stillActive(ctx context.Context, instanceID, chat string) error {
	cfg, ok := h.config(ctx, instanceID)
	if !ok || !cfg.Agents() {
		return ErrChatTaken
	}
	ca, err := h.chatAgent(ctx, instanceID, chat)
	if err != nil {
		return err
	}
	if !active(cfg, ca, h.opt.Clock.Now()) {
		return ErrChatTaken
	}
	if h.opt.Held != nil && h.opt.Held(instanceID) {
		return errors.New("sending is suspended by the operator")
	}
	return nil
}

// agentsDone logs an agents run; nothing is left to act on.
func (h *Hook) agentsDone(c store.AgentCall, out AgentsOutcome) bool {
	if out.NoMatch {
		return true
	}
	c.Status, c.Detail, c.Replied, c.ReplyID = "ok", out.Detail, out.Replied, out.ReplyID
	c.Actions = strings.Join(out.Actions, ",")
	h.record(c)
	return true
}
