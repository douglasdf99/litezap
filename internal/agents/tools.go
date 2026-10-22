package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/google/jsonschema-go/jsonschema"
)

// ToolDef is one entry of the MCP tool table (mcp.ToolDef, adapted by the caller so this
// package does not depend on the MCP server): its input schema and its in-process call,
// with the MCP validations and error hints.
type ToolDef struct {
	Name        string
	Description string
	Schema      func() (*jsonschema.Schema, error)
	Call        func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// hiddenArgs are tool arguments the executor owns: the instance is always the agent's,
// sends never wait, and idempotency keys are set by the engine.
var hiddenArgs = []string{"instance_id", "wait", "timeout_seconds", "client_message_id"}

// toolHiddenArgs are per-tool arguments the executor removes: agents read and send only
// the instance's own catalog (another business's catalog is third-party content, and a
// product of another catalog could be anything).
var toolHiddenArgs = map[string][]string{
	"get_catalog":  {"jid"},
	"get_product":  {"jid"},
	"send_product": {"catalog_owner"},
}

// dropHidden removes the executor-owned arguments of a tool from args or schema props.
func dropHidden(tool string, m map[string]any) {
	for _, k := range hiddenArgs {
		delete(m, k)
	}
	for _, k := range toolHiddenArgs[tool] {
		delete(m, k)
	}
}

// toolSpecs builds the tools offered to the model: the agent's allowed tools from the
// MCP table, with the executor-owned arguments removed from their schemas.
func toolSpecs(defs []ToolDef, allowed map[string]bool, readOther bool) ([]attendant.ToolSpec, map[string]ToolDef, error) {
	var specs []attendant.ToolSpec
	byName := map[string]ToolDef{}
	for _, d := range defs {
		if !allowed[d.Name] {
			continue
		}
		s, err := d.Schema()
		if err != nil {
			return nil, nil, fmt.Errorf("tool %s: %w", d.Name, err)
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil, err
		}
		props, _ := m["properties"].(map[string]any)
		dropHidden(d.Name, props)
		desc := d.Description
		optional := func(key, doc string) { // key defaults to the trigger chat
			if p, ok := props[key].(map[string]any); ok {
				p["description"] = doc
			}
			if req, ok := m["required"].([]any); ok {
				kept := []any{}
				for _, r := range req {
					if r != key {
						kept = append(kept, r)
					}
				}
				m["required"] = kept
			}
		}
		if sendTools[d.Name] {
			optional("to", "Recipient chat. Omit it to answer in the chat that triggered this run (the usual case). "+
				"Another chat is only allowed when this agent has the permission to send to other chats.")
		}
		if key, ok := chatReadTools[d.Name]; ok && !readOther {
			optional(key, "Omit it: this agent may only read the chat that triggered this run.")
		}
		raw, _ = json.Marshal(m)
		specs = append(specs, attendant.ToolSpec{Name: d.Name, Description: desc, Schema: raw})
		byName[d.Name] = d
	}
	return specs, byName, nil
}

// Step outcomes.
const (
	OutcomeRead     = "read"     // a read tool ran
	OutcomeExecuted = "executed" // a write tool ran (sends were queued)
	OutcomeDraft    = "draft"    // a write became a draft waiting for approval
	OutcomeNote     = "note"     // a write was recorded as a suggestion (mode sugerir)
	OutcomeDryRun   = "dry_run"  // simulator: what the write would do
	OutcomeRefused  = "refused"  // the executor refused the call (permission, limit)
	OutcomeError    = "error"    // the tool failed
)

// Step is one tool call of a run, as logged (arguments and result summarized).
type Step struct {
	N       int    `json:"n"` // model call that asked for it
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	Outcome string `json:"outcome"`
	Result  string `json:"result,omitempty"`
	DraftID string `json:"draft_id,omitempty"`
	// Chat is the recipient (sends) or the chat acted on.
	Chat string `json:"chat,omitempty"`
}

const (
	maxStepText   = 600      // logged args/result
	maxToolOutput = 16 << 10 // a tool result given back to the model
)

// executor runs one run's tool calls and enforces its restrictions.
type executor struct {
	r          *Runner
	instanceID string
	agent      Agent
	mode       string
	runID      string
	trigger    string // trigger chat (normalized), "" when none
	dryRun     bool
	guard      func(context.Context) error
	defs       map[string]ToolDef
	canary     string
	leaks      func(text, canary string) bool

	recipients   map[string]bool
	writes       int // write calls accepted (executed, drafted, suggested or simulated)
	sends        int
	groupChanges int
	n            int // write calls executed or drafted (idempotency keys)
	steps        []Step
	sent         int
	drafts       int
	replied      bool
	replyID      string
	actions      []string
	stopped      error // the guard refused: the run ends
}

// toolError is the content of a refused or failed call, as the model sees it.
func toolError(msg string) (string, bool) { return msg, true }

// exec runs one call and returns the tool result for the model.
func (e *executor) exec(ctx context.Context, step int, call attendant.ToolCall) (string, bool) {
	st := Step{N: step, Tool: call.Name, Args: clipText(string(call.Input), maxStepText)}
	content, isErr := e.run(ctx, call, &st)
	if st.Outcome == "" {
		st.Outcome = OutcomeError
		if !isErr {
			st.Outcome = OutcomeRead
		}
	}
	if st.Result == "" {
		st.Result = clipText(content, maxStepText)
	}
	e.steps = append(e.steps, st)
	return content, isErr
}

func (e *executor) refuse(st *Step, msg string) (string, bool) {
	st.Outcome = OutcomeRefused
	return toolError("refused by LiteZap: " + msg)
}

func (e *executor) run(ctx context.Context, call attendant.ToolCall, st *Step) (string, bool) {
	def, ok := e.defs[call.Name]
	if !ok {
		return e.refuse(st, "tool "+call.Name+" is not allowed for this agent")
	}
	if e.stopped != nil {
		return e.refuse(st, "the run was stopped: "+e.stopped.Error())
	}
	args := map[string]any{}
	if len(call.Input) > 0 && string(call.Input) != "null" {
		if err := json.Unmarshal(call.Input, &args); err != nil || args == nil {
			return toolError("the arguments must be a JSON object")
		}
	}
	dropHidden(call.Name, args)
	if !writeTools[call.Name] {
		if err := e.checkRead(call.Name, args); err != nil {
			return e.refuse(st, err.Error())
		}
		out, err := def.Call(ctx, mustJSON(args))
		if err != nil {
			st.Outcome = OutcomeError
			return toolError(err.Error())
		}
		if err := e.checkReadResult(call.Name, out); err != nil {
			return e.refuse(st, err.Error())
		}
		st.Outcome = OutcomeRead
		return string(out), false
	}
	// A write: permissions, recipient and caps, before the mode decides what happens.
	chat, err := e.checkWrite(call.Name, args)
	if err != nil {
		return e.refuse(st, err.Error())
	}
	st.Chat = chat
	if raw := mustJSON(args); e.leaks != nil && e.leaks(string(raw), e.canary) {
		return e.refuse(st, "the arguments contain the agent's instructions or a key")
	}
	switch {
	case e.dryRun:
		e.count(call.Name, chat)
		st.Outcome = OutcomeDryRun
		return string(mustJSON(map[string]any{"executed": false, "dry_run": true, "would": map[string]any{"tool": call.Name, "args": args},
			"note": "Simulation: nothing was sent or changed. Continue as if it had worked."})), false
	case e.mode == ModeSuggest:
		e.count(call.Name, chat)
		st.Outcome = OutcomeNote
		e.actions = append(e.actions, call.Name+":note")
		return string(mustJSON(map[string]any{"executed": false, "note": "This agent only suggests: the action was recorded " +
			"as a note for the operator and NOT executed. Do not try again; summarize your suggestion at the end."})), false
	case e.mode == ModeApproval:
		e.n++
		d, err := e.r.createDraft(ctx, e, call.Name, args, chat)
		if err != nil {
			st.Outcome = OutcomeError
			return toolError("could not save the draft: internal error")
		}
		e.count(call.Name, chat)
		st.Outcome, st.DraftID = OutcomeDraft, d.ID
		e.drafts++
		e.actions = append(e.actions, call.Name+":draft")
		return string(mustJSON(map[string]any{"executed": false, "draft_id": d.ID, "status": "pending_approval",
			"note": "Saved as a draft: a person will review it and approve or reject it. Do not repeat this call."})), false
	}
	// Autonomous: the chat must still be the agents' (a human may have taken over).
	if e.guard != nil {
		if err := e.guard(ctx); err != nil {
			e.stopped = err
			return e.refuse(st, err.Error())
		}
	}
	e.n++
	if sendTools[call.Name] {
		args["wait"] = false
		args["client_message_id"] = "agent:" + e.runID + ":" + strconv.Itoa(e.n)
	}
	e.count(call.Name, chat) // counted even if it fails: a retry loop must still hit the caps
	out, err := def.Call(ctx, mustJSON(args))
	if err != nil {
		st.Outcome = OutcomeError
		return toolError(err.Error())
	}
	st.Outcome = OutcomeExecuted
	e.sent++
	e.actions = append(e.actions, call.Name)
	if sendTools[call.Name] && chat == e.trigger {
		e.replied = true
		if e.replyID == "" {
			e.replyID = messageID(out)
		}
	}
	return string(out), false
}

// count records an accepted write in the run's caps.
func (e *executor) count(tool, chat string) {
	e.writes++
	if sendTools[tool] {
		e.sends++
		e.recipients[chat] = true
	}
	if groupChangeTools[tool] {
		e.groupChanges++
	}
}

// checkWrite resolves (and fills) the chat a write acts on and checks the agent's
// permissions and the run's caps. It is also run again when a draft is approved.
func (e *executor) checkWrite(tool string, args map[string]any) (string, error) {
	if !e.agent.allowedTools()[tool] {
		return "", errors.New("tool " + tool + " is not allowed for this agent")
	}
	chat, err := e.target(tool, args)
	if err != nil {
		return "", err
	}
	if sendTools[tool] {
		if err := checkMentions(args); err != nil {
			return "", err
		}
	}
	l := e.agent.Limits
	switch {
	case e.writes >= l.MaxWrites:
		return "", fmt.Errorf("at most %d write actions per run", l.MaxWrites)
	case sendTools[tool] && e.sends >= l.MaxSends:
		return "", fmt.Errorf("at most %d sends per run", l.MaxSends)
	case sendTools[tool] && !e.recipients[chat] && len(e.recipients) >= l.MaxRecipients:
		return "", fmt.Errorf("at most %d distinct recipients per run", l.MaxRecipients)
	case groupChangeTools[tool] && e.groupChanges >= l.MaxGroupChanges:
		return "", fmt.Errorf("at most %d group changes per run", l.MaxGroupChanges)
	}
	return chat, nil
}

// checkMentions refuses mentioning everyone, whatever its spelling, and malformed lists.
func checkMentions(args map[string]any) error {
	raw, ok := args["mentions"]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return errors.New("mentions must be a list of phone numbers")
	}
	for _, m := range list {
		s, ok := m.(string)
		if !ok {
			return errors.New("mentions must be a list of phone numbers")
		}
		if strings.EqualFold(strings.TrimSpace(s), "all") {
			return errors.New(`mentions ["all"] is not allowed for agents`)
		}
	}
	return nil
}

// target resolves (and fills) the chat a write acts on, enforcing the trigger-chat
// restriction of sends. label_chat (etiquetas) may label any chat; group tools act on
// the group they name and have their own quotas and checks.
func (e *executor) target(tool string, args map[string]any) (string, error) {
	switch tool {
	case "create_group":
		return "", nil
	case "update_group", "manage_group_participants", "get_group_invite_link":
		g, _ := args["group"].(string)
		return strings.TrimSpace(g), nil
	}
	key := "to"
	if tool == "label_chat" {
		key = "chat"
	}
	raw, _ := args[key].(string)
	chat := e.trigger
	if strings.TrimSpace(raw) == "" {
		if e.trigger == "" {
			return "", errors.New(key + " is required: this run has no trigger chat")
		}
	} else {
		j, err := service.ChatJID(raw)
		if err != nil {
			return "", fmt.Errorf("invalid %s: %v", key, err)
		}
		chat = j
	}
	args[key] = chat
	if sendTools[tool] && chat != e.trigger && !e.agent.has(GroupOtherChats) {
		if e.trigger == "" {
			return "", errors.New("this agent may only send in the chat that triggered it, and this run has none " +
				"(the permission to send to other chats is off)")
		}
		return "", errors.New("this agent may only send in the chat that triggered it (the permission to send to other chats is off)")
	}
	return chat, nil
}

// errReadOther refuses a read outside the trigger chat.
var errReadOther = errors.New("this agent may only read the chat that triggered it (the permission to read other chats is off)")

// checkRead limits per-chat reads to the trigger chat unless ler_outros_chats.
func (e *executor) checkRead(tool string, args map[string]any) error {
	key, ok := chatReadTools[tool]
	if !ok || e.agent.has(GroupReadOther) {
		return nil
	}
	raw, _ := args[key].(string)
	if strings.TrimSpace(raw) == "" {
		if e.trigger == "" {
			return errReadOther
		}
		args[key] = e.trigger
		return nil
	}
	j, err := service.ChatJID(raw)
	if err != nil || j != e.trigger {
		return errReadOther
	}
	args[key] = j
	return nil
}

// checkReadResult drops a message status of another chat (it carries its text).
func (e *executor) checkReadResult(tool string, out json.RawMessage) error {
	if tool != "get_message_status" || e.agent.has(GroupReadOther) {
		return nil
	}
	var v struct {
		Chat string `json:"chat"`
	}
	_ = json.Unmarshal(out, &v)
	if j, err := service.ChatJID(v.Chat); err != nil || e.trigger == "" || j != e.trigger {
		return errReadOther
	}
	return nil
}

// messageID extracts message.id from a send tool's output.
func messageID(out json.RawMessage) string {
	var v struct {
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	_ = json.Unmarshal(out, &v)
	return v.Message.ID
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// clipText shortens s to n bytes on a rune boundary.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
