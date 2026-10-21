package attendant

import (
	"context"
	"time"

	"github.com/douglasdf99/litezap/internal/events"
)

// AgentSession gives a LiteZap agent (tool-use loop, package agents) the instance's
// provider configuration: the same provider, encrypted keys, spend caps and usage log as
// the built-in attendant. Every model call must be preceded by Reserve.
type AgentSession struct {
	a          *Attendant
	instanceID string
	settings   Settings
	secrets    Secrets
	provider   ToolProvider
}

// AgentSession loads the instance's provider for an agent run. Its errors are
// *agenthook.CallError with reason not_configured when the provider is missing, has no
// key, or does not support tool use.
func (a *Attendant) AgentSession(ctx context.Context, instanceID string) (*AgentSession, error) {
	s, sec, ok, err := a.load(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notConfigured("no AI provider configured: set it up in the Atendente IA tab (admin)")
	}
	if why := s.ready(sec); why != "" {
		return nil, notConfigured(why)
	}
	backs, err := a.backends(s, sec)
	if err != nil {
		return nil, notConfigured(err.Error())
	}
	tp, ok := backs.Chat.(ToolProvider)
	if !ok || tp == nil {
		return nil, notConfigured("the configured provider does not support tool use")
	}
	return &AgentSession{a: a, instanceID: instanceID, settings: s, secrets: sec, provider: tp}, nil
}

// Model is the configured model.
func (s *AgentSession) Model() string { return s.settings.Model }

// MaxTokens bounds one model answer.
func (s *AgentSession) MaxTokens() int { return s.settings.MaxTokens }

// Reserve counts one model call against the chat's daily budget (or the simulator's,
// when simulate) and the instance's daily cap, atomically in the store. A cap reached is
// an *agenthook.CallError with reason daily_cap.
func (s *AgentSession) Reserve(ctx context.Context, chat string, simulate bool) error {
	in := runInput{instanceID: s.instanceID, chat: chat, settings: s.settings, simulate: simulate}
	return s.a.reserve(ctx, &in)
}

// ChatTools asks the provider for the next assistant turn (User is filled with the
// chat's pseudonym, never its phone number).
func (s *AgentSession) ChatTools(ctx context.Context, chat string, req ToolChatRequest) (ToolChatResponse, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = s.settings.MaxTokens
	}
	if chat != "" {
		req.User = s.a.Vault.Pseudonym(s.instanceID, chat)
	}
	return s.provider.ChatTools(ctx, req)
}

// AddUsage logs a call's tokens and returns its estimated cost in millionths of USD.
func (s *AgentSession) AddUsage(model string, in, out int64) int64 {
	if model == "" {
		model = s.settings.Model
	}
	return s.a.addUsage(s.instanceID, s.settings, model, in, out)
}

// Leaks reports text that echoes the canary, the system prompt or a provider key.
func (s *AgentSession) Leaks(text, canary string) bool { return leaks(text, canary, s.secrets) }

// Location is the instance's timezone.
func (s *AgentSession) Location(ctx context.Context) *time.Location {
	return s.a.loc(ctx, s.instanceID)
}

// Now is the attendant's clock.
func (s *AgentSession) Now() time.Time { return s.a.now() }

// Exported prompt helpers, so agents render untrusted text exactly like the attendant.

// Transcript renders the conversation (history, then the latest messages) inside markers
// with a random suffix, every message neutralized on one labeled line, followed by task.
func Transcript(history, latest []Line, contactName string, now time.Time, loc *time.Location, task string) string {
	return userTurn(history, latest, contactName, now, loc, task)
}

// Neutralize keeps untrusted text on one line, unable to forge a label or close a tag.
func Neutralize(s string) string { return neutralize(s) }

// NewCanary returns a random marker to put in a system prompt.
func NewCanary() string { return newCanary() }

// NewDelimiter returns a random suffix for markers around untrusted text.
func NewDelimiter() string { return newDelimiter() }

// Describe renders a message as text (attachments are named, not opened).
func Describe(typ, text string, md *events.Media) string { return describe(typ, text, md) }

// Clip shortens s to n runes.
func Clip(s string, n int) string { return clip(s, n) }

// LocalModel reports a small local model (Ollama or another loopback server): agents use
// a shorter prompt for it.
func (s *AgentSession) LocalModel() bool { return s.settings.LocalModel() }
