package agents

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/douglasdf99/litezap/internal/attendant"
)

// Tool-call validation before anything runs. Small local models sometimes call a tool
// that does not exist, write arguments that are not JSON or leave out required
// arguments. The run gives the model one retry with the reason; a second invalid turn in
// a row ends the run (status failed) instead of looping until the step limit.

// OutcomeInvalid is a call the executor never ran: it was malformed.
const OutcomeInvalid = "invalid"

// invalidCalls checks a turn's calls against the offered tools. It returns one problem
// per call ("" when the call is valid) and whether any call is invalid.
func invalidCalls(calls []attendant.ToolCall, specs []attendant.ToolSpec) ([]string, bool) {
	byName := make(map[string]attendant.ToolSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}
	out := make([]string, len(calls))
	bad := false
	for i, c := range calls {
		out[i] = checkCall(c, byName)
		if out[i] != "" {
			bad = true
		}
	}
	return out, bad
}

func checkCall(c attendant.ToolCall, byName map[string]attendant.ToolSpec) string {
	spec, ok := byName[c.Name]
	if !ok {
		names := make([]string, 0, len(byName))
		for n := range byName {
			names = append(names, n)
		}
		sort.Strings(names)
		name := c.Name
		if name == "" {
			name = "(empty)"
		}
		return fmt.Sprintf("the tool %q does not exist; the available tools are: %s", attendant.Clip(name, 60), strings.Join(names, ", "))
	}
	if c.Malformed {
		return "the arguments of " + c.Name + " are not a valid JSON object; send them as a JSON object such as {\"text\": \"...\"}"
	}
	var args map[string]any
	if err := json.Unmarshal(c.Input, &args); err != nil || args == nil {
		if len(c.Input) == 0 || string(c.Input) == "null" {
			args = map[string]any{}
		} else {
			return "the arguments of " + c.Name + " must be a JSON object"
		}
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(spec.Schema, &schema) != nil {
		return ""
	}
	for _, r := range schema.Required {
		if _, ok := args[r]; !ok {
			return fmt.Sprintf("%s needs the argument %q", c.Name, r)
		}
	}
	for k, v := range args {
		raw, ok := schema.Properties[k]
		if !ok {
			continue
		}
		var p struct {
			Type any `json:"type"`
		}
		_ = json.Unmarshal(raw, &p)
		if want, ok := p.Type.(string); ok && !hasType(v, want) {
			return fmt.Sprintf("the argument %q of %s must be of type %s", k, c.Name, want)
		}
	}
	return ""
}

// hasType checks a decoded JSON value against a JSON schema type.
func hasType(v any, want string) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return true
}

// sanitizeTurn is the assistant turn as it goes back to the provider: malformed
// arguments become {} (some servers parse the history's arguments).
func sanitizeTurn(t attendant.ToolTurn) attendant.ToolTurn {
	calls := make([]attendant.ToolCall, len(t.Calls))
	for i, c := range t.Calls {
		if c.Malformed {
			c.Input, c.Raw = json.RawMessage(`{}`), ""
		}
		calls[i] = c
	}
	t.Calls = calls
	return t
}

// retryTurn answers an invalid turn: nothing ran, here is why, call again.
func retryTurn(calls []attendant.ToolCall, problems []string) attendant.ToolTurn {
	results := make([]attendant.ToolResult, 0, len(calls))
	for i, c := range calls {
		msg := "not executed: another call of this turn was invalid. Send the calls again, all with valid arguments."
		if problems[i] != "" {
			msg = "invalid call, nothing was executed: " + problems[i] + ". Fix it and call again (only once)."
		}
		results = append(results, attendant.ToolResult{CallID: c.ID, Content: msg, IsError: true})
	}
	return attendant.ToolTurn{Role: "user", Results: results}
}

// logInvalid records an invalid turn's calls in the run's steps.
func (e *executor) logInvalid(step int, calls []attendant.ToolCall, problems []string) {
	for i, c := range calls {
		args := string(c.Input)
		if c.Malformed {
			args = c.Raw
		}
		res := problems[i]
		if res == "" {
			res = "not executed: another call of this turn was invalid"
		}
		e.steps = append(e.steps, Step{N: step, Tool: attendant.Clip(c.Name, 80), Args: clipText(args, maxStepText),
			Outcome: OutcomeInvalid, Result: clipText(res, maxStepText)})
	}
}
