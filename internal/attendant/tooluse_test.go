package attendant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testToolSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text"],
"properties":{"to":{"type":"string"},"text":{"type":"string"}}}`)

// TestAnthropicToolUse runs a two-step tool-use exchange against a fake Messages API
// (base_url): the tools and the tool_use/tool_result round trip use the API's shapes.
func TestAnthropicToolUse(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "sk-test" {
			http.Error(w, "bad request "+r.URL.Path, 400)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5",
				"content":[{"type":"text","text":"vou responder"},{"type":"tool_use","id":"tu_1","name":"send_message","input":{"text":"oi"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5-5",
			"content":[{"type":"text","text":"pronto"}],"stop_reason":"end_turn","usage":{"input_tokens":150,"output_tokens":5}}`)
	}))
	defer srv.Close()
	b, err := DefaultFactory(Settings{Provider: ProviderAnthropic, Model: DefaultModel, BaseURL: srv.URL}, Secrets{APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	tp := b.Chat.(ToolProvider)
	req := ToolChatRequest{System: "sys", MaxTokens: 500, User: "lz_abc",
		Tools: []ToolSpec{{Name: "send_message", Description: "send", Schema: testToolSchema}},
		Turns: []ToolTurn{{Role: "user", Text: "nova mensagem"}}}
	r1, err := tp.ChatTools(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r1.StopReason != "tool_use" || len(r1.Turn.Calls) != 1 || r1.Turn.Calls[0].Name != "send_message" ||
		string(r1.Turn.Calls[0].Input) != `{"text":"oi"}` || r1.Turn.Text != "vou responder" || r1.InputTokens != 100 {
		t.Fatalf("first answer %+v", r1)
	}
	tools := bodies[0]["tools"].([]any)
	tool := tools[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if tool["name"] != "send_message" || schema["type"] != "object" || schema["additionalProperties"] != false ||
		schema["required"].([]any)[0] != "text" {
		t.Fatalf("tool sent %v", tool)
	}
	req.Turns = append(req.Turns, r1.Turn, ToolTurn{Role: "user", Results: []ToolResult{{CallID: "tu_1", Content: `{"queued":true}`}}})
	r2, err := tp.ChatTools(context.Background(), req)
	if err != nil || r2.StopReason != "end_turn" || r2.Turn.Text != "pronto" || len(r2.Turn.Calls) != 0 {
		t.Fatalf("second answer %+v %v", r2, err)
	}
	msgs := bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages %v", msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["id"] != "tu_1" {
		t.Fatalf("assistant turn sent back %v", asst)
	}
	res := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["type"] != "tool_result" || res["tool_use_id"] != "tu_1" {
		t.Fatalf("tool result %v", res)
	}
}

// TestOpenAIFunctionCalling is the same exchange over an OpenAI-compatible server.
func TestOpenAIFunctionCalling(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function",
				"function":{"name":"send_message","arguments":"{\"text\":\"oi\"}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"feito"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	c := &openAIChat{base: srv.URL, model: "m", client: httpClient()}
	req := ToolChatRequest{System: "sys", MaxTokens: 100, Tools: []ToolSpec{{Name: "send_message", Schema: testToolSchema}},
		Turns: []ToolTurn{{Role: "user", Text: "oi"}}}
	r1, err := c.ChatTools(context.Background(), req)
	if err != nil || r1.StopReason != "tool_use" || len(r1.Turn.Calls) != 1 || string(r1.Turn.Calls[0].Input) != `{"text":"oi"}` {
		t.Fatalf("first %+v %v", r1, err)
	}
	fn := bodies[0]["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "send_message" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("tools %v", bodies[0]["tools"])
	}
	req.Turns = append(req.Turns, r1.Turn, ToolTurn{Role: "user", Results: []ToolResult{{CallID: "c1", Content: "ok"}}})
	r2, err := c.ChatTools(context.Background(), req)
	if err != nil || r2.StopReason != "end_turn" || r2.Turn.Text != "feito" {
		t.Fatalf("second %+v %v", r2, err)
	}
	msgs := bodies[1]["messages"].([]any)
	if m := msgs[2].(map[string]any); m["role"] != "assistant" || m["tool_calls"] == nil {
		t.Fatalf("assistant %v", m)
	}
	if m := msgs[3].(map[string]any); m["role"] != "tool" || m["tool_call_id"] != "c1" || m["content"] != "ok" {
		t.Fatalf("tool message %v", m)
	}
}
