package attendant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// Tool use (LiteZap agents): the model calls tools in a loop. The types are provider
// neutral; each provider maps them to its own API (Messages API tool use on Anthropic,
// function calling on OpenAI-compatible servers).

// ToolSpec is a tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema of the input (an object)
}

// ToolCall is a tool the model asked to run.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
	// Malformed: the model's arguments were not a JSON object (small local models do
	// that); Input is then {} and Raw holds what the model wrote.
	Malformed bool
	Raw       string
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// ToolTurn is one turn of a tool-use conversation: user text, the assistant's text and
// calls, or the results of those calls (a user turn).
type ToolTurn struct {
	Role    string // user | assistant
	Text    string
	Calls   []ToolCall
	Results []ToolResult
	// Raw is the provider's own assistant content (thinking blocks included), sent back
	// as is on the next request; nil for turns built by the caller.
	Raw any
}

// ToolChatRequest asks for the next assistant turn.
type ToolChatRequest struct {
	System    string
	Turns     []ToolTurn
	Tools     []ToolSpec
	MaxTokens int
	User      string // pseudonymous end-user id
}

// ToolChatResponse is the assistant turn.
type ToolChatResponse struct {
	Turn ToolTurn
	// StopReason: end_turn | tool_use | max_tokens | refusal | other.
	StopReason   string
	Model        string
	InputTokens  int64
	OutputTokens int64
	Refused      bool
}

// ToolProvider is a provider that supports tool use.
type ToolProvider interface {
	ChatTools(ctx context.Context, req ToolChatRequest) (ToolChatResponse, error)
}

var (
	_ ToolProvider = (*anthropicChat)(nil)
	_ ToolProvider = (*openAIChat)(nil)
)

// schemaParts splits a JSON schema into the properties, required list and other keywords.
func schemaParts(raw json.RawMessage) (props any, required []string, extra map[string]any, err error) {
	var m map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil, nil, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, nil, fmt.Errorf("tool schema: %w", err)
	}
	props = m["properties"]
	if props == nil {
		props = map[string]any{}
	}
	if rs, ok := m["required"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				required = append(required, s)
			}
		}
	}
	for k, v := range m {
		switch k {
		case "properties", "required", "type", "$schema":
		default:
			if extra == nil {
				extra = map[string]any{}
			}
			extra[k] = v
		}
	}
	return props, required, extra, nil
}

func (c *anthropicChat) ChatTools(ctx context.Context, req ToolChatRequest) (ToolChatResponse, error) {
	msgs := make([]anthropic.BetaMessageParam, 0, len(req.Turns))
	for _, t := range req.Turns {
		if t.Role == "assistant" {
			if raw, ok := t.Raw.(anthropic.BetaMessageParam); ok {
				msgs = append(msgs, raw)
				continue
			}
			var blocks []anthropic.BetaContentBlockParamUnion
			if t.Text != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(t.Text))
			}
			for _, call := range t.Calls {
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(call.ID, call.Input, call.Name))
			}
			if len(blocks) > 0 {
				msgs = append(msgs, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
			}
			continue
		}
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, r := range t.Results {
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		if t.Text != "" {
			blocks = append(blocks, anthropic.NewBetaTextBlock(t.Text))
		}
		if len(blocks) > 0 {
			msgs = append(msgs, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleUser, Content: blocks})
		}
	}
	tools := make([]anthropic.BetaToolUnionParam, 0, len(req.Tools))
	for _, ts := range req.Tools {
		props, required, extra, err := schemaParts(ts.Schema)
		if err != nil {
			return ToolChatResponse{}, err
		}
		tp := anthropic.BetaToolParam{Name: ts.Name, InputSchema: anthropic.BetaToolInputSchemaParam{Properties: props,
			Required: required, ExtraFields: extra}}
		if ts.Description != "" {
			tp.Description = anthropic.String(ts.Description)
		}
		tools = append(tools, anthropic.BetaToolUnionParam{OfTool: &tp})
	}
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(c.model),
		MaxTokens:    int64(req.MaxTokens),
		System:       []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:     msgs,
		Tools:        tools,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort("low")},
	}
	if req.User != "" {
		params.Metadata = anthropic.BetaMetadataParam{UserID: anthropic.String(req.User)}
	}
	if c.official && fallbackModel(c.model) {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return ToolChatResponse{}, cleanProviderErr(err)
	}
	out := ToolChatResponse{Model: string(resp.Model), StopReason: string(resp.StopReason),
		InputTokens:  resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
		OutputTokens: resp.Usage.OutputTokens}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		out.Refused = true
		return out, nil
	}
	turn := ToolTurn{Role: "assistant", Raw: resp.ToParam()}
	var texts []string
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			texts = append(texts, b.Text)
		case anthropic.BetaToolUseBlock:
			in, err := json.Marshal(b.Input)
			if err != nil {
				in = json.RawMessage(`{}`)
			}
			turn.Calls = append(turn.Calls, ToolCall{ID: b.ID, Name: b.Name, Input: in})
		}
	}
	turn.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	out.Turn = turn
	return out, nil
}

// ChatTools uses OpenAI function calling (/chat/completions with tools).
func (c *openAIChat) ChatTools(ctx context.Context, req ToolChatRequest) (ToolChatResponse, error) {
	type fnCall struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function fnCall `json:"function"`
	}
	type message struct {
		Role       string     `json:"role"`
		Content    *string    `json:"content"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	str := func(s string) *string { return &s }
	msgs := []message{{Role: "system", Content: str(req.System)}}
	for _, t := range req.Turns {
		switch {
		case t.Role == "assistant":
			m := message{Role: "assistant"}
			if t.Text != "" || len(t.Calls) == 0 {
				m.Content = str(t.Text)
			}
			for _, call := range t.Calls {
				args := string(call.Input)
				if args == "" {
					args = "{}"
				}
				m.ToolCalls = append(m.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: fnCall{Name: call.Name, Arguments: args}})
			}
			msgs = append(msgs, m)
		default:
			for _, r := range t.Results {
				content := r.Content
				if r.IsError {
					content = "ERROR: " + content
				}
				msgs = append(msgs, message{Role: "tool", ToolCallID: r.CallID, Content: str(content)})
			}
			if t.Text != "" {
				msgs = append(msgs, message{Role: "user", Content: str(t.Text)})
			}
		}
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, ts := range req.Tools {
		var schema any = map[string]any{"type": "object", "properties": map[string]any{}}
		if len(ts.Schema) > 0 {
			if err := json.Unmarshal(ts.Schema, &schema); err != nil {
				return ToolChatResponse{}, fmt.Errorf("tool schema: %w", err)
			}
		}
		tools = append(tools, map[string]any{"type": "function",
			"function": map[string]any{"name": ts.Name, "description": ts.Description, "parameters": schema}})
	}
	body := map[string]any{"model": c.model, "messages": msgs, "max_tokens": req.MaxTokens}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.User != "" && !c.noUser {
		body["user"] = req.User
	}
	raw, status, err := c.post(ctx, "/chat/completions", body)
	if err != nil {
		return ToolChatResponse{}, err
	}
	if status == http.StatusBadRequest && len(tools) > 0 {
		return ToolChatResponse{}, errors.New("chat API: HTTP 400 (does this server and model support function calling?)")
	}
	if status < 200 || status > 299 {
		return ToolChatResponse{}, fmt.Errorf("chat API: HTTP %d", status)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string         `json:"content"`
				Refusal   string         `json:"refusal"`
				ToolCalls []respToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return ToolChatResponse{}, errors.New("chat API: unexpected response")
	}
	ch := out.Choices[0]
	r := ToolChatResponse{Model: out.Model, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if ch.Message.Refusal != "" || ch.FinishReason == "content_filter" {
		r.Refused, r.StopReason = true, "refusal"
		return r, nil
	}
	turn := ToolTurn{Role: "assistant", Text: strings.TrimSpace(ch.Message.Content)}
	for i, tc := range ch.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		turn.Calls = append(turn.Calls, newToolCall(id, tc.Function.Name, tc.Function.Arguments))
	}
	if len(turn.Calls) == 0 && len(req.Tools) > 0 {
		// Small local models sometimes write the call as text instead of tool_calls.
		offered := map[string]bool{}
		for _, ts := range req.Tools {
			offered[ts.Name] = true
		}
		if calls, rest, ok := textToolCalls(turn.Text, offered); ok {
			turn.Calls, turn.Text = calls, rest
		}
	}
	r.Turn = turn
	switch {
	case len(turn.Calls) > 0:
		r.StopReason = "tool_use"
	case ch.FinishReason == "length":
		r.StopReason = "max_tokens"
	default:
		r.StopReason = "end_turn"
	}
	return r, nil
}

// respToolCall is a tool call as servers answer it: arguments are a JSON string per the
// OpenAI API, but some compatible servers send the object itself.
type respToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// newToolCall normalizes a call's arguments (string or object, fenced, with text around
// it); arguments that are not a JSON object mark the call malformed.
func newToolCall(id, name string, raw json.RawMessage) ToolCall {
	text := strings.TrimSpace(string(raw))
	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = strings.TrimSpace(s)
	}
	call := ToolCall{ID: id, Name: strings.TrimSpace(name), Input: json.RawMessage(`{}`)}
	if obj, ok := jsonObject(text); ok {
		call.Input = obj
		return call
	}
	call.Malformed, call.Raw = true, clip(text, 2000)
	return call
}

// jsonObject extracts a JSON object from text: as is, inside a code fence, or between
// the first { and the last }. Empty text and null are an empty object.
func jsonObject(text string) (json.RawMessage, bool) {
	t := strings.TrimSpace(text)
	if t == "" || t == "null" {
		return json.RawMessage(`{}`), true
	}
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimPrefix(t, "json")
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	try := func(c string) (json.RawMessage, bool) {
		var m map[string]any
		if json.Unmarshal([]byte(c), &m) != nil || m == nil {
			return nil, false
		}
		return json.RawMessage(c), true
	}
	if obj, ok := try(t); ok {
		return obj, true
	}
	if i, j := strings.Index(t, "{"), strings.LastIndex(t, "}"); i >= 0 && j > i {
		return try(t[i : j+1])
	}
	return nil, false
}

// textToolCalls finds tool calls a model wrote as text: <tool_call>{...}</tool_call>
// blocks (Qwen, Hermes) or a whole answer that is {"name": ..., "arguments"|"parameters":
// {...}} (Llama), alone or in a list. Only offered tools count; rest is the other text.
func textToolCalls(text string, offered map[string]bool) ([]ToolCall, string, bool) {
	type textCall struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
	}
	conv := func(i int, c textCall) (ToolCall, bool) {
		if !offered[strings.TrimSpace(c.Name)] {
			return ToolCall{}, false
		}
		args := c.Arguments
		if len(args) == 0 {
			args = c.Parameters
		}
		return newToolCall(fmt.Sprintf("call_t%d", i), c.Name, args), true
	}
	var calls []ToolCall
	if strings.Contains(text, "<tool_call>") {
		rest := text
		for {
			i := strings.Index(rest, "<tool_call>")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "</tool_call>")
			end := len(rest)
			body := rest[i+len("<tool_call>"):]
			if j >= 0 {
				end = i + j + len("</tool_call>")
				body = rest[i+len("<tool_call>") : i+j]
			}
			var c textCall
			if obj, ok := jsonObject(body); ok && json.Unmarshal(obj, &c) == nil {
				if call, ok := conv(len(calls), c); ok {
					calls = append(calls, call)
				}
			}
			rest = rest[:i] + rest[end:]
		}
		if len(calls) > 0 {
			return calls, strings.TrimSpace(rest), true
		}
		return nil, text, false
	}
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimPrefix(t, "json")
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	var list []textCall
	if strings.HasPrefix(t, "[") {
		if json.Unmarshal([]byte(t), &list) != nil {
			return nil, text, false
		}
	} else {
		var c textCall
		if !strings.HasPrefix(t, "{") || json.Unmarshal([]byte(t), &c) != nil {
			return nil, text, false
		}
		list = []textCall{c}
	}
	for _, c := range list {
		call, ok := conv(len(calls), c)
		if !ok {
			return nil, text, false
		}
		calls = append(calls, call)
	}
	return calls, "", len(calls) > 0
}
