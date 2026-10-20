package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// Event types (registered in the events catalog).
const (
	TypeRunCompleted = "agent.run.completed"
	TypeDraftCreated = "agent.draft.created"
	TypeDraftDecided = "agent.draft.decided"
)

func init() {
	events.Types = append(events.Types, TypeRunCompleted, TypeDraftCreated, TypeDraftDecided)
}

// Session is what a run needs from the instance's AI provider (attendant.AgentSession).
type Session interface {
	Reserve(ctx context.Context, chat string, simulate bool) error
	ChatTools(ctx context.Context, chat string, req attendant.ToolChatRequest) (attendant.ToolChatResponse, error)
	AddUsage(model string, in, out int64) int64
	Leaks(text, canary string) bool
	Location(ctx context.Context) *time.Location
	Model() string
}

var _ Session = (*attendant.AgentSession)(nil)

// Sessions opens the provider session of an instance.
type Sessions func(ctx context.Context, instanceID string) (Session, error)

// FromAttendant adapts the built-in attendant (provider settings, keys, caps).
func FromAttendant(a *attendant.Attendant) Sessions {
	return func(ctx context.Context, id string) (Session, error) {
		s, err := a.AgentSession(ctx, id)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
}

// Runner runs agents and keeps their records. It is the agent hook's "agents" target and
// the service behind the /agents API.
type Runner struct {
	Store    *store.Store
	Sessions Sessions
	// Tools returns the MCP tool table scoped to one instance (mcp.Catalog over a Local
	// backend with that instance's scope, adapted to ToolDef).
	Tools  func(instanceID string) []ToolDef
	Events events.Publisher // nil: no events
	Clock  func() time.Time
	Log    *slog.Logger

	mu       sync.Mutex
	chats    map[string]chan struct{} // instance|chat -> held while a run is in flight
	tools    sync.Map                 // instance -> []ToolDef (stateless, built once)
	decideMu sync.Mutex               // serializes draft approvals (the run's caps)
}

// toolsFor returns the instance's tool table, built once (its schemas resolve on first use).
func (r *Runner) toolsFor(instanceID string) []ToolDef {
	if v, ok := r.tools.Load(instanceID); ok {
		return v.([]ToolDef)
	}
	v, _ := r.tools.LoadOrStore(instanceID, r.Tools(instanceID))
	return v.([]ToolDef)
}

var _ agenthook.AgentsRunner = (*Runner)(nil)

func (r *Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (r *Runner) publish(instanceID, typ string, data any) {
	if r.Events != nil {
		r.Events.Publish(instanceID, typ, data)
	}
}

// lockChat takes the chat's run slot: one run at a time per chat, whatever triggered it.
// With wait false it fails at once when the slot is taken.
func (r *Runner) lockChat(ctx context.Context, instanceID, chat string, wait bool) (func(), error) {
	if chat == "" {
		return func() {}, nil
	}
	key := instanceID + "|" + chat
	for {
		r.mu.Lock()
		if r.chats == nil {
			r.chats = map[string]chan struct{}{}
		}
		ch, busy := r.chats[key]
		if !busy {
			ch = make(chan struct{})
			r.chats[key] = ch
			r.mu.Unlock()
			return func() {
				r.mu.Lock()
				delete(r.chats, key)
				r.mu.Unlock()
				close(ch)
			}, nil
		}
		r.mu.Unlock()
		if !wait {
			return nil, apierr.New(http.StatusConflict, "agent_busy", "an agent run is already in progress in this chat", true)
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// RunInput is one run to perform.
type RunInput struct {
	Agent      Agent
	InstanceID string
	Trigger    string // message | manual (the simulator uses DryRun)
	Chat       string // trigger chat ("" for a manual run without one)
	ChatName   string
	IsGroup    bool
	MessageID  string
	Context    []agenthook.ContextMessage
	Input      string // operator's request (manual runs, simulator)
	DryRun     bool   // simulator: writes are never executed, nothing is stored
	// Guard is called before each autonomous write (nil: always allowed).
	Guard func(context.Context) error
}

// RunResult is a finished run.
type RunResult struct {
	Run    store.AgentRun
	Steps  []Step
	exec   *executor
	Agent  Agent
	DryRun bool
}

// Run statuses.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusStepLimit = "step_limit"
	StatusFailed    = "failed"
	StatusDiscarded = "discarded" // a human took over during the run
)

// run performs the tool-use loop. Provider errors before the first call (not
// configured) are returned; later failures end the run with status failed.
func (r *Runner) run(ctx context.Context, in RunInput) (RunResult, error) {
	a := in.Agent
	sess, err := r.Sessions(ctx, in.InstanceID)
	if err != nil {
		return RunResult{}, err
	}
	defs := r.toolsFor(in.InstanceID)
	specs, byName, err := toolSpecs(defs, a.allowedTools(), a.has(GroupReadOther))
	if err != nil {
		return RunResult{}, err
	}
	trigger := in.Chat
	if trigger != "" {
		if j, err := service.ChatJID(trigger); err == nil {
			trigger = j
		}
	}
	now := r.now()
	run := store.AgentRun{ID: secure.NewID("arun_"), InstanceID: in.InstanceID, AgentID: a.ID, AgentName: a.Name,
		Trigger: in.Trigger, ChatJID: in.Chat, MessageID: in.MessageID, Mode: a.Mode, Status: StatusRunning,
		Model: sess.Model(), StartedAt: now}
	if !in.DryRun {
		if err := r.Store.InsertAgentRun(ctx, run); err != nil {
			return RunResult{}, err
		}
	}
	canary := attendant.NewCanary()
	ex := &executor{r: r, instanceID: in.InstanceID, agent: a, mode: a.Mode, runID: run.ID, trigger: trigger,
		dryRun: in.DryRun, guard: in.Guard, defs: byName, canary: canary, leaks: sess.Leaks, recipients: map[string]bool{}}
	req := attendant.ToolChatRequest{System: systemPrompt(a, a.Mode, in.DryRun, canary), Tools: specs,
		Turns: []attendant.ToolTurn{{Role: "user", Text: userTurn(in, now, sess.Location(ctx))}}}
	delim := attendant.NewDelimiter()
	run.Status = StatusStepLimit
	for step := 1; step <= a.Limits.MaxSteps; step++ {
		if err := sess.Reserve(ctx, in.Chat, in.DryRun); err != nil {
			run.Status, run.Error = StatusFailed, errText(err)
			break
		}
		resp, err := sess.ChatTools(ctx, in.Chat, req)
		run.ModelCalls++
		if resp.Model != "" {
			run.Model = resp.Model
		}
		if resp.InputTokens > 0 || resp.OutputTokens > 0 {
			run.InputTokens += resp.InputTokens
			run.OutputTokens += resp.OutputTokens
			run.CostMicros += sess.AddUsage(resp.Model, resp.InputTokens, resp.OutputTokens)
		}
		if err != nil {
			if ctx.Err() != nil {
				run.Status, run.Error = StatusFailed, "timeout: the run took too long"
			} else {
				run.Status, run.Error = StatusFailed, "llm_error: "+err.Error()
			}
			break
		}
		if resp.Refused {
			run.Status, run.Error = StatusFailed, "refused: the model declined to continue"
			break
		}
		req.Turns = append(req.Turns, resp.Turn)
		if len(resp.Turn.Calls) == 0 {
			run.Output = resp.Turn.Text
			run.Status = StatusCompleted
			break
		}
		results := make([]attendant.ToolResult, 0, len(resp.Turn.Calls))
		for _, call := range resp.Turn.Calls {
			content, isErr := ex.exec(ctx, step, call)
			results = append(results, attendant.ToolResult{CallID: call.ID, Content: wrapToolOutput(content, delim), IsError: isErr})
		}
		req.Turns = append(req.Turns, attendant.ToolTurn{Role: "user", Results: results})
		if ex.stopped != nil {
			run.Status, run.Error = StatusDiscarded, ex.stopped.Error()
			break
		}
		if resp.Turn.Text != "" {
			run.Output = resp.Turn.Text
		}
	}
	if run.Status == StatusStepLimit {
		run.Error = fmt.Sprintf("step limit reached (%d model calls)", a.Limits.MaxSteps)
	}
	if sess.Leaks(run.Output, canary) {
		run.Output = "(resumo bloqueado: continha as instruções do agente ou uma chave)"
	}
	run.Output = clipText(strings.TrimSpace(run.Output), 4000)
	run.Sent, run.Drafts = ex.sent, ex.drafts
	run.EndedAt = r.now()
	stored := redactSteps(ex.steps)
	stepsJSON, _ := json.Marshal(stored)
	run.StepsJSON = clipSteps(stepsJSON, stored)
	res := RunResult{Run: run, Steps: ex.steps, exec: ex, Agent: a, DryRun: in.DryRun}
	if in.DryRun {
		return res, nil
	}
	if err := r.Store.FinishAgentRun(context.WithoutCancel(ctx), run); err != nil {
		r.log().Error("agents: save run", "instance_id", in.InstanceID, "run_id", run.ID, "err", err)
	}
	r.publish(in.InstanceID, TypeRunCompleted, map[string]any{"run_id": run.ID, "agent_id": a.ID, "agent_name": a.Name,
		"trigger": run.Trigger, "chat": run.ChatJID, "message_id": run.MessageID, "mode": run.Mode, "status": run.Status,
		"steps": len(ex.steps), "model_calls": run.ModelCalls, "sent": run.Sent, "drafts": run.Drafts,
		"cost_usd": usd(run.CostMicros), "error": run.Error})
	return res, nil
}

// phoneRe matches phone numbers (and the user part of phone JIDs).
var phoneRe = regexp.MustCompile(`\d{8,}`)

// maskPhones keeps the last 4 digits of every phone number.
func maskPhones(s string) string {
	return phoneRe.ReplaceAllStringFunc(s, func(d string) string { return "••••" + d[len(d)-4:] })
}

// redactSteps is what the run log keeps: tool results (third-party messages, contacts)
// only for refused or failed calls, the reason, with phone numbers masked.
func redactSteps(steps []Step) []Step {
	out := make([]Step, len(steps))
	for i, s := range steps {
		if s.Outcome == OutcomeRefused || s.Outcome == OutcomeError {
			s.Result = maskPhones(s.Result)
		} else {
			s.Result = ""
		}
		out[i] = s
	}
	return out
}

// draftEvent is the data of agent.draft.created and agent.draft.decided: ids and status
// only, never the message or the arguments (they stay in the API, behind the token).
func draftEvent(d store.AgentDraft, status string) map[string]any {
	return map[string]any{"draft_id": d.ID, "agent_id": d.AgentID, "tool": d.Tool, "chat": d.ChatJID, "status": status}
}

// clipSteps keeps the stored steps under 64KB (the oldest are kept, then a marker).
func clipSteps(raw []byte, steps []Step) string {
	const limit = 64 << 10
	for len(raw) > limit && len(steps) > 1 {
		steps = steps[:len(steps)/2]
		raw, _ = json.Marshal(append(steps, Step{Tool: "…", Outcome: "truncated"}))
	}
	return string(raw)
}

func errText(err error) string {
	var ce *agenthook.CallError
	if errors.As(err, &ce) {
		return ce.Reason + ": " + ce.Detail
	}
	return err.Error()
}

func usd(micros int64) float64 { return float64(micros) / 1e6 }

// createDraft stores a write call for approval and announces it.
func (r *Runner) createDraft(ctx context.Context, e *executor, tool string, args map[string]any, chat string) (store.AgentDraft, error) {
	d := store.AgentDraft{ID: secure.NewID("adr_"), InstanceID: e.instanceID, AgentID: e.agent.ID, RunID: e.runID, Tool: tool,
		ChatJID: chat, Status: DraftPending, CreatedAt: r.now()}
	if sendTools[tool] {
		args["wait"] = false
		args["client_message_id"] = "agentdraft:" + d.ID // one send per draft, never cancelled by a human pause
	}
	d.ArgsJSON = string(mustJSON(args))
	if err := r.Store.CreateAgentDraft(ctx, d); err != nil {
		r.log().Error("agents: save draft", "instance_id", e.instanceID, "err", err)
		return d, err
	}
	r.publish(e.instanceID, TypeDraftCreated, draftEvent(d, DraftPending))
	return d, nil
}

// publicArgs drops the engine's own fields from shown arguments.
func publicArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		switch k {
		case "wait", "client_message_id", "instance_id", "timeout_seconds":
			continue
		}
		out[k] = v
	}
	return out
}

// RunForMessage is the agent hook's "agents" target: it picks the first enabled agent
// whose trigger matches the message and runs it.
func (r *Runner) RunForMessage(ctx context.Context, req agenthook.Request, guard func(context.Context) error) (agenthook.AgentsOutcome, error) {
	var m events.Message
	_ = json.Unmarshal(req.Message, &m)
	list, err := r.loadAgents(ctx, req.InstanceID)
	if err != nil {
		return agenthook.AgentsOutcome{}, err
	}
	var chosen *Agent
	for i := range list {
		a := &list[i]
		ok, err := r.matches(ctx, req.InstanceID, *a, req.Chat, m)
		if err != nil {
			return agenthook.AgentsOutcome{}, err
		}
		if ok {
			chosen = a
			break
		}
	}
	if chosen == nil {
		return agenthook.AgentsOutcome{NoMatch: true}, nil
	}
	if err := r.underDailyRuns(ctx, req.InstanceID, *chosen); err != nil {
		return agenthook.AgentsOutcome{}, err
	}
	unlock, err := r.lockChat(ctx, req.InstanceID, req.Chat.JID, true)
	if err != nil {
		return agenthook.AgentsOutcome{}, err
	}
	defer unlock()
	res, err := r.run(ctx, RunInput{Agent: *chosen, InstanceID: req.InstanceID, Trigger: TriggerMessage, Chat: req.Chat.JID,
		ChatName: req.Chat.Name, IsGroup: req.Chat.IsGroup, MessageID: m.ID, Context: req.Context, Guard: guard})
	if err != nil {
		return agenthook.AgentsOutcome{}, err
	}
	run := res.Run
	if run.Status == StatusFailed && strings.HasPrefix(run.Error, "daily_cap") {
		return agenthook.AgentsOutcome{}, &agenthook.CallError{Reason: "daily_cap", Detail: strings.TrimPrefix(run.Error, "daily_cap: ")}
	}
	detail := fmt.Sprintf("agente %s · %s · %s · %d passo(s)", chosen.Name, run.ID, run.Status, len(res.Steps))
	if run.Error != "" {
		detail += " · " + run.Error
	}
	return agenthook.AgentsOutcome{Replied: res.exec.replied, ReplyID: res.exec.replyID, Detail: detail,
		Actions: res.exec.actions}, nil
}

// underDailyRuns refuses a run past the agent's daily limit.
func (r *Runner) underDailyRuns(ctx context.Context, instanceID string, a Agent) error {
	if a.Limits.DailyRuns <= 0 {
		return nil
	}
	day := r.now().UTC().Truncate(24 * time.Hour)
	stats, err := r.Store.AgentStatsSince(ctx, instanceID, day)
	if err != nil {
		return err
	}
	if stats[a.ID].Runs >= a.Limits.DailyRuns {
		return &agenthook.CallError{Reason: "daily_cap", Detail: fmt.Sprintf("agent %q reached its limit of %d runs today", a.Name, a.Limits.DailyRuns)}
	}
	return nil
}

// matches applies an agent's trigger filters to a received message.
func (r *Runner) matches(ctx context.Context, instanceID string, a Agent, chat agenthook.ChatInfo, m events.Message) (bool, error) {
	t := a.Trigger
	isGroup := chat.IsGroup || strings.HasSuffix(chat.JID, "@g.us")
	switch {
	case !a.Enabled, t.Type != TriggerMessage, isGroup && !t.Groups, m.FromMe:
		return false, nil
	}
	if len(t.Chats) > 0 && !containsChat(t.Chats, chat.JID) {
		return false, nil
	}
	if len(t.Keywords) > 0 {
		text := strings.ToLower(m.Text)
		hit := false
		for _, k := range t.Keywords {
			if strings.Contains(text, strings.ToLower(k)) {
				hit = true
				break
			}
		}
		if !hit {
			return false, nil
		}
	}
	if len(t.Labels) > 0 {
		ids, err := r.Store.ChatLabels(ctx, instanceID, chat.JID)
		if err != nil {
			return false, err
		}
		if len(ids) == 0 {
			return false, nil
		}
		labels, err := r.Store.ListLabels(ctx, instanceID)
		if err != nil {
			return false, err
		}
		names := map[string]string{}
		for _, l := range labels {
			names[l.ID] = strings.ToLower(l.Name)
		}
		hit := false
		for _, id := range ids {
			for _, want := range t.Labels {
				if want == id || strings.ToLower(want) == names[id] {
					hit = true
				}
			}
		}
		if !hit {
			return false, nil
		}
	}
	return true, nil
}

func containsChat(list []string, jid string) bool {
	n, err := service.ChatJID(jid)
	if err != nil {
		n = jid
	}
	for _, c := range list {
		if c == n || c == jid {
			return true
		}
	}
	return false
}
