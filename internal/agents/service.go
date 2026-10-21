package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// The use cases of the /agents API.

// View is an agent as the API shows it.
type View struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	Mode         string    `json:"mode"`
	Instructions string    `json:"instructions"`
	Tools        []string  `json:"tools"`
	Trigger      Trigger   `json:"trigger"`
	Limits       Limits    `json:"limits"`
	Runtime      string    `json:"runtime"`
	Today        DayStats  `json:"today"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DayStats are an agent's runs and estimated cost today (UTC).
type DayStats struct {
	Runs    int     `json:"runs"`
	CostUSD float64 `json:"cost_usd"`
}

func view(a Agent, st DayStats) View {
	return View{ID: a.ID, Name: a.Name, Enabled: a.Enabled, Mode: a.Mode, Instructions: a.Instructions, Tools: a.Tools,
		Trigger: a.Trigger, Limits: a.Limits, Runtime: a.Runtime, Today: st, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

func decodeAgent(row store.AgentRow) (Agent, error) {
	a := defaultAgent()
	a.ID, a.Name, a.Enabled, a.Mode, a.Instructions = row.ID, row.Name, row.Enabled, row.Mode, row.Instructions
	if row.Runtime != "" {
		a.Runtime = row.Runtime
	}
	a.CreatedAt, a.UpdatedAt = row.CreatedAt, row.UpdatedAt
	if err := json.Unmarshal([]byte(row.ToolsJSON), &a.Tools); err != nil {
		return a, fmt.Errorf("stored agent tools: %w", err)
	}
	if err := json.Unmarshal([]byte(row.TriggerJSON), &a.Trigger); err != nil {
		return a, fmt.Errorf("stored agent trigger: %w", err)
	}
	if err := json.Unmarshal([]byte(row.LimitsJSON), &a.Limits); err != nil {
		return a, fmt.Errorf("stored agent limits: %w", err)
	}
	for _, p := range []*[]string{&a.Tools, &a.Trigger.Chats, &a.Trigger.Labels, &a.Trigger.Keywords} {
		if *p == nil {
			*p = []string{}
		}
	}
	return a, nil
}

func encodeAgent(instanceID string, a Agent) store.AgentRow {
	tools, _ := json.Marshal(a.Tools)
	trig, _ := json.Marshal(a.Trigger)
	lim, _ := json.Marshal(a.Limits)
	return store.AgentRow{ID: a.ID, InstanceID: instanceID, Name: a.Name, Enabled: a.Enabled, Mode: a.Mode,
		Instructions: a.Instructions, ToolsJSON: string(tools), TriggerJSON: string(trig), LimitsJSON: string(lim), Runtime: a.Runtime}
}

func (r *Runner) instance(ctx context.Context, id string) error {
	if _, err := r.Store.GetInstance(ctx, id); errors.Is(err, store.ErrNotFound) {
		return apierr.InstanceNotFound()
	} else if err != nil {
		return err
	}
	return nil
}

func agentNotFound() error {
	return apierr.New(http.StatusNotFound, "not_found", "agent not found", false)
}

func (r *Runner) loadAgents(ctx context.Context, instanceID string) ([]Agent, error) {
	rows, err := r.Store.ListAgents(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(rows))
	for _, row := range rows {
		a, err := decodeAgent(row)
		if err != nil {
			r.log().Error("agents: bad stored agent", "instance_id", instanceID, "agent_id", row.ID, "err", err)
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *Runner) getAgent(ctx context.Context, instanceID, id string) (Agent, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return Agent{}, err
	}
	row, err := r.Store.GetAgent(ctx, instanceID, id)
	if errors.Is(err, store.ErrNotFound) {
		return Agent{}, agentNotFound()
	}
	if err != nil {
		return Agent{}, err
	}
	return decodeAgent(row)
}

func (r *Runner) today(ctx context.Context, instanceID string) map[string]DayStats {
	out := map[string]DayStats{}
	stats, err := r.Store.AgentStatsSince(ctx, instanceID, r.now().UTC().Truncate(24*time.Hour))
	if err != nil {
		r.log().Error("agents: stats", "instance_id", instanceID, "err", err)
		return out
	}
	for id, s := range stats {
		out[id] = DayStats{Runs: s.Runs, CostUSD: usd(s.CostMicros)}
	}
	return out
}

// List returns the instance's agents with today's runs and cost.
func (r *Runner) List(ctx context.Context, instanceID string) ([]View, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	list, err := r.loadAgents(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	st := r.today(ctx, instanceID)
	out := make([]View, 0, len(list))
	for _, a := range list {
		out = append(out, view(a, st[a.ID]))
	}
	return out, nil
}

func (r *Runner) Get(ctx context.Context, instanceID, id string) (View, error) {
	a, err := r.getAgent(ctx, instanceID, id)
	if err != nil {
		return View{}, err
	}
	return view(a, r.today(ctx, instanceID)[a.ID]), nil
}

// Create adds an agent (defaults: read + reply in the trigger chat, aprovacao, new 1:1
// messages, disabled until enabled is true).
func (r *Runner) Create(ctx context.Context, instanceID string, in Input) (View, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return View{}, err
	}
	rows, err := r.Store.ListAgents(ctx, instanceID)
	if err != nil {
		return View{}, err
	}
	if len(rows) >= maxAgents {
		return View{}, apierr.Validation(fmt.Sprintf("at most %d agents per instance", maxAgents))
	}
	a := in.apply(defaultAgent())
	if err := a.validate(); err != nil {
		return View{}, apierr.Validation(err.Error())
	}
	if err := checkRuntimeNotice(ctx, RuntimeProvider, a.Runtime, in); err != nil {
		return View{}, err
	}
	a.ID = secure.NewID("agn_")
	if err := r.Store.CreateAgent(ctx, encodeAgent(instanceID, a)); err != nil {
		return View{}, err
	}
	return r.Get(ctx, instanceID, a.ID)
}

// Update changes the given fields of an agent (omitted fields keep their value).
func (r *Runner) Update(ctx context.Context, instanceID, id string, in Input) (View, error) {
	a, err := r.getAgent(ctx, instanceID, id)
	if err != nil {
		return View{}, err
	}
	prev := a.Runtime
	a = in.apply(a)
	if err := a.validate(); err != nil {
		return View{}, apierr.Validation(err.Error())
	}
	if err := checkRuntimeNotice(ctx, prev, a.Runtime, in); err != nil {
		return View{}, err
	}
	if err := r.Store.UpdateAgent(ctx, encodeAgent(instanceID, a)); err != nil {
		return View{}, err
	}
	return r.Get(ctx, instanceID, id)
}

// Delete removes an agent with its runs and drafts.
func (r *Runner) Delete(ctx context.Context, instanceID, id string) error {
	if _, err := r.getAgent(ctx, instanceID, id); err != nil {
		return err
	}
	return r.Store.DeleteAgent(ctx, instanceID, id)
}

// ---- runs ----

// RunView is a run as the API shows it.
type RunView struct {
	ID           string     `json:"id"`
	AgentID      string     `json:"agent_id"`
	AgentName    string     `json:"agent_name"`
	Trigger      string     `json:"trigger"`
	Chat         string     `json:"chat,omitempty"`
	MessageID    string     `json:"message_id,omitempty"`
	Mode         string     `json:"mode"`
	Status       string     `json:"status"`
	Steps        []Step     `json:"steps"`
	Output       string     `json:"output"`
	Model        string     `json:"model,omitempty"`
	ModelCalls   int        `json:"model_calls"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	Sent         int        `json:"sent"`
	Drafts       int        `json:"drafts"`
	Error        string     `json:"error,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DryRun       bool       `json:"dry_run,omitempty"`
}

func runView(run store.AgentRun) RunView {
	v := RunView{ID: run.ID, AgentID: run.AgentID, AgentName: run.AgentName, Trigger: run.Trigger, Chat: run.ChatJID,
		MessageID: run.MessageID, Mode: run.Mode, Status: run.Status, Steps: []Step{}, Output: run.Output, Model: run.Model,
		ModelCalls: run.ModelCalls, InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, CostUSD: usd(run.CostMicros),
		Sent: run.Sent, Drafts: run.Drafts, Error: run.Error, StartedAt: run.StartedAt}
	_ = json.Unmarshal([]byte(run.StepsJSON), &v.Steps)
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	if !run.EndedAt.IsZero() {
		t := run.EndedAt
		v.EndedAt = &t
	}
	return v
}

// Runs lists an agent's recent runs (every agent's with agentID ""), newest first.
func (r *Runner) Runs(ctx context.Context, instanceID, agentID string, limit int) ([]RunView, error) {
	if agentID != "" {
		if _, err := r.getAgent(ctx, instanceID, agentID); err != nil {
			return nil, err
		}
	} else if err := r.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.Store.ListAgentRuns(ctx, instanceID, agentID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(rows))
	for _, row := range rows {
		out = append(out, runView(row))
	}
	return out, nil
}

func (r *Runner) Run(ctx context.Context, instanceID, runID string) (RunView, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return RunView{}, err
	}
	row, err := r.Store.GetAgentRun(ctx, instanceID, runID)
	if errors.Is(err, store.ErrNotFound) {
		return RunView{}, apierr.New(http.StatusNotFound, "not_found", "run not found", false)
	}
	if err != nil {
		return RunView{}, err
	}
	return runView(row), nil
}

// RunNowInput is a manual run ("Executar agora").
type RunNowInput struct {
	// Chat, when set, is the trigger chat (its recent messages are the context and the
	// reply tools answer there).
	Chat string `json:"chat,omitempty"`
	// Input is an optional request for this run, from the operator.
	Input string `json:"input,omitempty"`
}

// manualTimeout bounds a manual run (it outlives the HTTP request if the client leaves).
const manualTimeout = 3 * time.Minute

// RunNow runs an agent now (enabled or not), waiting for the result.
func (r *Runner) RunNow(ctx context.Context, instanceID, agentID string, in RunNowInput) (RunView, error) {
	a, err := r.getAgent(ctx, instanceID, agentID)
	if err != nil {
		return RunView{}, err
	}
	if len(in.Input) > 4000 {
		return RunView{}, apierr.Validation("input is limited to 4000 characters")
	}
	ri := RunInput{Agent: a, InstanceID: instanceID, Trigger: TriggerManual, Input: in.Input}
	if strings.TrimSpace(in.Chat) != "" {
		chat, err := service.ChatJID(in.Chat)
		if err != nil {
			return RunView{}, err
		}
		ri.Chat, ri.IsGroup = chat, strings.HasSuffix(chat, "@g.us")
		if ca, err := r.Store.GetChatAgent(ctx, instanceID, chat); err == nil {
			ri.ChatName = ca.Name
		}
		if ri.Context, err = r.chatContext(ctx, instanceID, chat, 20); err != nil {
			return RunView{}, err
		}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), manualTimeout)
	defer cancel()
	unlock, err := r.lockChat(rctx, instanceID, ri.Chat, false)
	if err != nil {
		return RunView{}, err
	}
	defer unlock()
	if err := r.underDailyRuns(rctx, instanceID, a); err != nil {
		return RunView{}, callErr(err)
	}
	res, err := r.run(rctx, ri)
	if err != nil {
		return RunView{}, callErr(err)
	}
	return runView(res.Run), nil
}

// callErr turns provider-setup errors into API errors.
func callErr(err error) error {
	var ce *agenthook.CallError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Reason {
	case "not_configured":
		return apierr.New(http.StatusConflict, "not_configured", ce.Detail, false)
	case "daily_cap":
		return apierr.New(http.StatusTooManyRequests, "rate_limited", ce.Detail, true)
	}
	return apierr.New(http.StatusBadGateway, ce.Reason, ce.Detail, true)
}

// chatContext loads a chat's last n messages, oldest first, labeled by origin.
func (r *Runner) chatContext(ctx context.Context, instanceID, chat string, n int) ([]agenthook.ContextMessage, error) {
	msgs, _, err := r.Store.ListMessages(ctx, instanceID, chat, n, nil)
	if err != nil {
		return nil, err
	}
	var apiIDs []string
	for _, m := range msgs {
		if m.FromMe && m.Source == "api" {
			apiIDs = append(apiIDs, m.ID)
		}
	}
	clientIDs, err := r.Store.ClientIDsForMessages(ctx, instanceID, apiIDs)
	if err != nil {
		return nil, err
	}
	out := []agenthook.ContextMessage{}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		out = append(out, agenthook.ContextMessage{ID: m.ID, Sender: m.SenderJID, FromMe: m.FromMe, Source: m.Source,
			Origin: agenthook.Origin(m.FromMe, m.Source, clientIDs[m.ID]), Type: m.Type, Text: m.Text, Timestamp: m.Timestamp,
			Revoked: !m.RevokedAt.IsZero()})
	}
	return out, nil
}

// ---- simulator ----

// SimMessage is one message of a sample conversation.
type SimMessage struct {
	// From: customer (default) | agent | human | automatic.
	From string `json:"from"`
	Text string `json:"text"`
}

// SimulateInput runs a saved (agent_id) or unsaved (agent) agent on a sample
// conversation; write tools are always dry runs.
type SimulateInput struct {
	AgentID  string       `json:"agent_id,omitempty"`
	Agent    *Input       `json:"agent,omitempty"`
	Messages []SimMessage `json:"messages"`
	// Chat, when set, is used as the trigger chat (read tools see its real data).
	Chat  string `json:"chat,omitempty"`
	Input string `json:"input,omitempty"`
}

// SimChat is the trigger chat of a simulation without one (never written to).
const SimChat = "5500000000000@s.whatsapp.net"

var simOrigins = map[string]string{"": "contact", "customer": "contact", "agent": "agent", "human": "human", "automatic": "automatic"}

// Simulate runs the loop without executing any write and without storing the run.
func (r *Runner) Simulate(ctx context.Context, instanceID string, in SimulateInput) (RunView, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return RunView{}, err
	}
	var a Agent
	switch {
	case in.Agent != nil:
		a = in.Agent.apply(defaultAgent())
		if in.AgentID != "" {
			stored, err := r.getAgent(ctx, instanceID, in.AgentID)
			if err != nil {
				return RunView{}, err
			}
			a = in.Agent.apply(stored)
		}
		if a.Name == "" {
			a.Name = "Agente (simulação)"
		}
		prev := RuntimeProvider
		if in.AgentID != "" {
			if stored, err := r.getAgent(ctx, instanceID, in.AgentID); err == nil {
				prev = stored.Runtime
			}
		}
		if err := a.validate(); err != nil {
			return RunView{}, apierr.Validation(err.Error())
		}
		if err := checkRuntimeNotice(ctx, prev, a.Runtime, *in.Agent); err != nil {
			return RunView{}, err
		}
	case in.AgentID != "":
		var err error
		if a, err = r.getAgent(ctx, instanceID, in.AgentID); err != nil {
			return RunView{}, err
		}
	default:
		return RunView{}, apierr.Validation("agent_id or agent is required")
	}
	if len(in.Messages) > 50 || len(in.Input) > 4000 {
		return RunView{}, apierr.Validation("at most 50 messages and 4000 characters of input")
	}
	ri := RunInput{Agent: a, InstanceID: instanceID, Trigger: TriggerMessage, Chat: SimChat, Input: in.Input, DryRun: true}
	if len(in.Messages) == 0 {
		ri.Trigger = TriggerManual
	}
	if strings.TrimSpace(in.Chat) != "" {
		chat, err := service.ChatJID(in.Chat)
		if err != nil {
			return RunView{}, err
		}
		ri.Chat, ri.IsGroup = chat, strings.HasSuffix(chat, "@g.us")
	}
	now := r.now()
	for i, m := range in.Messages {
		origin, ok := simOrigins[m.From]
		if !ok {
			return RunView{}, apierr.Validation(`messages[].from must be "customer", "agent", "human" or "automatic"`)
		}
		ri.Context = append(ri.Context, agenthook.ContextMessage{ID: fmt.Sprintf("sim_%d", i), Origin: origin, Type: "text",
			Text: m.Text, FromMe: origin != "contact", Timestamp: now.Add(time.Duration(i-len(in.Messages)) * time.Minute)})
	}
	res, err := r.run(ctx, ri)
	if err != nil {
		return RunView{}, callErr(err)
	}
	v := runView(res.Run)
	v.Steps, v.DryRun = res.Steps, true
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	v.ID = ""
	return v, nil
}

// ---- drafts ----

// Draft statuses.
const (
	DraftPending   = "pending"
	DraftApproving = "approving"
	DraftApproved  = "approved"
	DraftRejected  = "rejected"
	DraftFailed    = "failed"
	DraftExpired   = "expired"
)

// DraftTTL is how long a draft can be approved (a reply a day late does more harm).
const DraftTTL = 24 * time.Hour

// DraftView is a draft as the API shows it: the exact call that approval executes.
type DraftView struct {
	ID        string         `json:"id"`
	AgentID   string         `json:"agent_id"`
	AgentName string         `json:"agent_name,omitempty"`
	RunID     string         `json:"run_id"`
	Tool      string         `json:"tool"`
	Chat      string         `json:"chat,omitempty"`
	Args      map[string]any `json:"args"`
	Status    string         `json:"status"`
	DecidedBy string         `json:"decided_by,omitempty"`
	DecidedAt *time.Time     `json:"decided_at,omitempty"`
	Result    string         `json:"result,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

func draftView(d store.AgentDraft, names map[string]string) DraftView {
	v := DraftView{ID: d.ID, AgentID: d.AgentID, AgentName: names[d.AgentID], RunID: d.RunID, Tool: d.Tool, Chat: d.ChatJID,
		Status: d.Status, DecidedBy: d.DecidedBy, Result: d.Result, CreatedAt: d.CreatedAt, ExpiresAt: d.CreatedAt.Add(DraftTTL)}
	var args map[string]any
	_ = json.Unmarshal([]byte(d.ArgsJSON), &args)
	v.Args = publicArgs(args)
	if !d.DecidedAt.IsZero() {
		t := d.DecidedAt
		v.DecidedAt = &t
	}
	return v
}

func (r *Runner) agentNames(ctx context.Context, instanceID string) map[string]string {
	out := map[string]string{}
	rows, err := r.Store.ListAgents(ctx, instanceID)
	if err == nil {
		for _, row := range rows {
			out[row.ID] = row.Name
		}
	}
	return out
}

// Drafts lists drafts (one status, or all with ""), newest first.
func (r *Runner) Drafts(ctx context.Context, instanceID, status string, limit int) ([]DraftView, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return nil, err
	}
	switch status {
	case "", DraftPending, DraftApproved, DraftRejected, DraftFailed, DraftExpired:
	default:
		return nil, apierr.Validation("status must be pending, approved, rejected, failed or expired")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.Store.ListAgentDrafts(ctx, instanceID, status, limit)
	if err != nil {
		return nil, err
	}
	names := r.agentNames(ctx, instanceID)
	out := make([]DraftView, 0, len(rows))
	now := r.now()
	for _, d := range rows {
		if d.Status == DraftPending && now.Sub(d.CreatedAt) > DraftTTL {
			if status == DraftPending {
				continue // expired: no longer approvable
			}
			d.Status = DraftExpired
		}
		out = append(out, draftView(d, names))
	}
	return out, nil
}

// Decide approves (executing exactly the stored call, through the send queue) or rejects
// a pending draft.
func (r *Runner) Decide(ctx context.Context, instanceID, draftID string, approve bool, decidedBy string) (DraftView, error) {
	if err := r.instance(ctx, instanceID); err != nil {
		return DraftView{}, err
	}
	d, err := r.Store.GetAgentDraft(ctx, instanceID, draftID)
	if errors.Is(err, store.ErrNotFound) {
		return DraftView{}, apierr.New(http.StatusNotFound, "not_found", "draft not found", false)
	}
	if err != nil {
		return DraftView{}, err
	}
	conflict := func(msg string) error { return apierr.New(http.StatusConflict, "conflict", msg, false) }
	if d.Status != DraftPending {
		return DraftView{}, conflict("this draft was already decided (" + d.Status + ")")
	}
	now := r.now()
	if now.Sub(d.CreatedAt) > DraftTTL {
		if ok, _ := r.Store.ClaimAgentDraft(ctx, instanceID, draftID, DraftExpired, decidedBy, now); ok {
			_ = r.Store.SettleAgentDraft(ctx, instanceID, draftID, DraftExpired, "expired after 24h")
		}
		return DraftView{}, conflict("this draft expired (drafts can be approved for 24 hours)")
	}
	next := DraftRejected
	if approve {
		next = DraftApproving
		// One approval at a time: the run's caps count the drafts already approved.
		r.decideMu.Lock()
		defer r.decideMu.Unlock()
		if err := r.revalidate(ctx, instanceID, d); err != nil {
			return DraftView{}, apierr.New(http.StatusConflict, "conflict", "cannot approve: "+err.Error(), false)
		}
	}
	ok, err := r.Store.ClaimAgentDraft(ctx, instanceID, draftID, next, decidedBy, now)
	if err != nil {
		return DraftView{}, err
	}
	if !ok {
		return DraftView{}, conflict("this draft was already decided")
	}
	status, result := DraftRejected, ""
	if approve {
		status, result = r.execDraft(context.WithoutCancel(ctx), instanceID, d)
	}
	if err := r.Store.SettleAgentDraft(context.WithoutCancel(ctx), instanceID, draftID, status, result); err != nil {
		return DraftView{}, err
	}
	d.Status, d.Result, d.DecidedBy, d.DecidedAt = status, result, decidedBy, now
	names := r.agentNames(ctx, instanceID)
	v := draftView(d, names)
	r.publish(instanceID, TypeDraftDecided, draftEvent(d, status))
	return v, nil
}

// revalidate checks a draft again before it is executed: the agent must still exist, be
// enabled and hold the permission, and the stored call must still pass the recipient,
// mentions and caps checks (counting the run's drafts already approved).
func (r *Runner) revalidate(ctx context.Context, instanceID string, d store.AgentDraft) error {
	row, err := r.Store.GetAgent(ctx, instanceID, d.AgentID)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("the agent was deleted")
	}
	if err != nil {
		return err
	}
	a, err := decodeAgent(row)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return errors.New("the agent is paused: turn it on to approve its drafts")
	}
	run, err := r.Store.GetAgentRun(ctx, instanceID, d.RunID)
	if err != nil {
		return err
	}
	trigger := run.ChatJID
	if j, err := service.ChatJID(trigger); err == nil {
		trigger = j
	}
	ex := &executor{agent: a, trigger: trigger, recipients: map[string]bool{}}
	siblings, err := r.Store.ListAgentDraftsForRun(ctx, instanceID, d.RunID)
	if err != nil {
		return err
	}
	for _, s := range siblings {
		if s.ID != d.ID && (s.Status == DraftApproved || s.Status == DraftApproving) {
			ex.count(s.Tool, s.ChatJID)
		}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(d.ArgsJSON), &args); err != nil || args == nil {
		return errors.New("the stored call is corrupt")
	}
	chat, err := ex.checkWrite(d.Tool, args)
	if err != nil {
		return err
	}
	if chat != d.ChatJID {
		return errors.New("the recipient no longer matches the draft")
	}
	return nil
}

// RecoverDrafts finishes the drafts left in "approving" by a crash (at startup): their
// call is executed again, which is safe since a send's client_message_id makes it
// idempotent.
func (r *Runner) RecoverDrafts(ctx context.Context) {
	list, err := r.Store.ListAgentDraftsByStatus(ctx, DraftApproving, 500)
	if err != nil {
		r.log().Error("agents: list approving drafts", "err", err)
		return
	}
	for _, d := range list {
		status, result := r.execDraft(ctx, d.InstanceID, d)
		if err := r.Store.SettleAgentDraft(ctx, d.InstanceID, d.ID, status, result); err != nil {
			r.log().Error("agents: recover draft", "draft_id", d.ID, "err", err)
			continue
		}
		r.publish(d.InstanceID, TypeDraftDecided, draftEvent(d, status))
	}
}

// execDraft runs the stored call with the instance's tool table.
func (r *Runner) execDraft(ctx context.Context, instanceID string, d store.AgentDraft) (string, string) {
	for _, def := range r.toolsFor(instanceID) {
		if def.Name != d.Tool {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		out, err := def.Call(cctx, json.RawMessage(d.ArgsJSON))
		if err != nil {
			return DraftFailed, clipText(err.Error(), 1000)
		}
		if id := messageID(out); id != "" {
			return DraftApproved, "queued as message " + id
		}
		return DraftApproved, clipText(string(out), 1000)
	}
	return DraftFailed, "tool " + d.Tool + " is not available"
}
