package mcp

import (
	"context"
	"net/http"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AttendantBackend is the optional read-only built-in attendant part of a Backend (spec
// 4.7). Changing agents or the provider stays in the REST API and the dashboard.
type AttendantBackend interface {
	AttendantSettings(ctx context.Context, id string) (attendant.SettingsView, error)
	AttendantAgents(ctx context.Context, id string) ([]attendant.AgentView, error)
	AttendantAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error)
}

// AttendantService is the subset of attendant.Admin used by Local.
type AttendantService interface {
	GetSettings(ctx context.Context, id string) (attendant.SettingsView, error)
	ListAgents(ctx context.Context, id string) ([]attendant.AgentView, error)
	GetAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error)
}

var (
	_ AttendantBackend = (*Local)(nil)
	_ AttendantBackend = (*REST)(nil)
)

func (l *Local) attendant(id string) (AttendantService, error) {
	if err := l.allow(id); err != nil {
		return nil, err
	}
	if l.Attendant == nil {
		return nil, apierr.New(http.StatusNotImplemented, "not_implemented", "not available on this server", false)
	}
	return l.Attendant, nil
}

func (l *Local) AttendantSettings(ctx context.Context, id string) (attendant.SettingsView, error) {
	a, err := l.attendant(id)
	if err != nil {
		return attendant.SettingsView{}, err
	}
	return a.GetSettings(ctx, id)
}

func (l *Local) AttendantAgents(ctx context.Context, id string) ([]attendant.AgentView, error) {
	a, err := l.attendant(id)
	if err != nil {
		return nil, err
	}
	return a.ListAgents(ctx, id)
}

func (l *Local) AttendantAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error) {
	a, err := l.attendant(id)
	if err != nil {
		return attendant.AgentView{}, err
	}
	return a.GetAgent(ctx, id, agentID)
}

func (r *REST) AttendantSettings(ctx context.Context, id string) (attendant.SettingsView, error) {
	var v attendant.SettingsView
	err := r.call(ctx, restTimeout, "GET", instPath(id, "attendant"), nil, nil, &v)
	return v, err
}

func (r *REST) AttendantAgents(ctx context.Context, id string) ([]attendant.AgentView, error) {
	var out struct {
		Agents []attendant.AgentView `json:"agents"`
	}
	err := r.call(ctx, restTimeout, "GET", instPath(id, "attendant", "agents"), nil, nil, &out)
	return out.Agents, err
}

func (r *REST) AttendantAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error) {
	var v attendant.AgentView
	if agentID == "" {
		return v, apierr.Validation("agent_id is required")
	}
	err := r.call(ctx, restTimeout, "GET", instPath(id, "attendant", "agents", agentID), nil, nil, &v)
	return v, err
}

// ---- tools ----

type attendantInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance of the attendant"`
}

// attendantAgentSummary is an agent as listed by get_attendant (no knowledge base).
type attendantAgentSummary struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Mode       string   `json:"mode"`
	Active     bool     `json:"active"`
	Actions    []string `json:"actions"`
	Knowledge  int      `json:"knowledge_entries"`
	VoiceReply string   `json:"voice_reply"`
	CallFollow bool     `json:"call_followup"`
}

type attendantOutput struct {
	Provider      string                  `json:"provider"`
	Model         string                  `json:"model"`
	Ready         bool                    `json:"ready"`
	NotReady      string                  `json:"not_ready,omitempty"`
	DailyCallCap  int                     `json:"daily_call_cap"`
	CallsToday    int                     `json:"calls_today"`
	CostTodayUSD  float64                 `json:"cost_today_usd"`
	Transcription bool                    `json:"transcription"`
	Speech        bool                    `json:"speech"`
	Agents        []attendantAgentSummary `json:"agents"`
}

type attendantAgentInput struct {
	InstanceID string `json:"instance_id,omitempty" jsonschema:"Instance of the attendant"`
	AgentID    string `json:"agent_id" jsonschema:"Agent id (agt_...) from get_attendant"`
}

// registerAttendant adds the read-only attendant tools when the backend supports them.
func (t *tools) registerAttendant(s *sdk.Server) {
	ab, ok := t.b.(AttendantBackend)
	if !ok {
		return
	}
	instID := func(p *jsonschema.Schema) { p.Description = instanceIDDoc }
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	add(t, s, &sdk.Tool{
		Name:  "get_attendant",
		Title: "Get the built-in AI attendant",
		Description: "Shows the instance's built-in AI attendant (Atendente LiteZap): provider and model (keys are never shown), whether it is ready, " +
			"today's provider calls and estimated cost against the daily cap, and its agents (mode, allowed actions, which one answers). " +
			"Whether it answers chats is the agent hook's target \"builtin\". Read-only: agents are edited in the dashboard or the REST API.",
		InputSchema: schema[attendantInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: readOnly,
	}, func(ctx context.Context, in attendantInput) (*sdk.CallToolResult, attendantOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, attendantOutput{}, err
		}
		v, err := ab.AttendantSettings(ctx, id)
		if err != nil {
			return nil, attendantOutput{}, err
		}
		agents, err := ab.AttendantAgents(ctx, id)
		if err != nil {
			return nil, attendantOutput{}, err
		}
		out := attendantOutput{Provider: v.Provider, Model: v.Model, Ready: v.Ready, NotReady: v.NotReady,
			DailyCallCap: v.DailyCallCap, CallsToday: v.Today.Calls, CostTodayUSD: v.Today.CostUSD,
			Transcription: v.Transcription, Speech: v.Speech, Agents: []attendantAgentSummary{}}
		for _, a := range agents {
			out.Agents = append(out.Agents, attendantAgentSummary{ID: a.ID, Name: a.Name, Mode: a.Mode, Active: a.Active,
				Actions: a.Actions, Knowledge: len(a.Knowledge), VoiceReply: a.VoiceReply, CallFollow: a.CallFollowUp})
		}
		return nil, out, nil
	})

	add(t, s, &sdk.Tool{
		Name:  "get_attendant_agent",
		Title: "Get an AI attendant agent",
		Description: "Returns one agent of the built-in AI attendant: mode, objective, script steps, tone, handoff criteria, allowed actions, " +
			"business context and knowledge base (Q&A, catalog with prices, hours, policies). Read-only.",
		InputSchema: schema[attendantAgentInput](map[string]func(*jsonschema.Schema){"instance_id": instID}),
		Annotations: readOnly,
	}, func(ctx context.Context, in attendantAgentInput) (*sdk.CallToolResult, attendant.AgentView, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, attendant.AgentView{}, err
		}
		v, err := ab.AttendantAgent(ctx, id, in.AgentID)
		return nil, v, err
	})
}
