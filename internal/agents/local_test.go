package agents

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/douglasdf99/litezap/internal/attendant"
)

// fakeOllama is an OpenAI-compatible /v1/chat/completions that answers scripted raw
// messages, the way a small local model does (messy tool calls included).
type fakeOllama struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	answer func(n int) string // the JSON "message" object of answer n (from 1)
}

func useFakeOllama(t *testing.T, e *env) *fakeOllama {
	f := &fakeOllama{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		n := len(f.bodies)
		f.mu.Unlock()
		msg := `{"content":"nada a fazer"}`
		if f.answer != nil {
			msg = f.answer(n)
		}
		_, _ = io.WriteString(w, `{"model":"qwen2.5:7b","choices":[{"message":`+msg+`,"finish_reason":"stop"}],"usage":{"prompt_tokens":500,"completion_tokens":50}}`)
	}))
	t.Cleanup(f.srv.Close)
	if _, err := e.att.PutSettings(e.ctx, e.inst, attendant.SettingsPatch{Preset: ptr(attendant.PresetOllama),
		BaseURL: ptr(f.srv.URL + "/v1"), APIKey: ptr("")}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeOllama) body(i int) map[string]any { f.mu.Lock(); defer f.mu.Unlock(); return f.bodies[i] }
func (f *fakeOllama) calls() int                { f.mu.Lock(); defer f.mu.Unlock(); return len(f.bodies) }

const brokenSend = `{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"send_message","arguments":"{\"text\": \"Olá"}}]}`

// TestLocalModelRetriesAnInvalidCallOnce: malformed arguments are not executed; the model
// gets the reason, fixes the call, and the run goes on. The prompt is the short
// Portuguese one with the tool list, and local calls cost nothing.
func TestLocalModelRetriesAnInvalidCallOnce(t *testing.T) {
	e := newEnv(t)
	f := useFakeOllama(t, e)
	f.answer = func(n int) string {
		switch n {
		case 1:
			return brokenSend
		case 2:
			// Qwen style: the fixed call written as text.
			return `{"content":"<tool_call>\n{\"name\": \"send_message\", \"arguments\": {\"text\": \"Olá! Como posso ajudar?\"}}\n</tool_call>"}`
		}
		return `{"content":"Respondi o cliente."}`
	}
	a := e.agent(Input{Name: ptr("Local"), Enabled: ptr(true), Mode: ptr(ModeAutonomous)})
	e.receive(contact, "oi")
	run := e.lastRun(a.ID)
	if run.Status != StatusCompleted || run.Sent != 1 || f.calls() != 3 || run.CostUSD != 0 {
		t.Fatalf("run %+v calls=%d", run, f.calls())
	}
	if len(run.Steps) != 2 || run.Steps[0].Outcome != OutcomeInvalid || !strings.Contains(run.Steps[0].Result, "not a valid JSON object") ||
		run.Steps[1].Outcome != OutcomeExecuted {
		t.Fatalf("steps %+v", run.Steps)
	}
	e.wait("the reply", func() bool { return strings.Contains(joined(e.sentTexts()), "Como posso ajudar?") })
	sys := f.body(0)["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "Ferramentas disponíveis") || !strings.Contains(sys, "- send_message:") || strings.Contains(sys, "# Security rules") {
		t.Fatalf("local prompt:\n%s", sys)
	}
	// The retry request carries the error for the broken call, and the history's
	// arguments were replaced by {} (servers parse them).
	msgs := f.body(1)["messages"].([]any)
	asst, tool := msgs[2].(map[string]any), msgs[3].(map[string]any)
	args := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"]
	if args != "{}" || tool["role"] != "tool" || !strings.Contains(tool["content"].(string), "invalid call") {
		t.Fatalf("retry turn %v / %v", asst, tool)
	}
}

// TestLocalModelFailsAfterTwoInvalidTurns: a model that keeps sending invalid calls ends
// the run (failed) instead of burning every step, and nothing is sent.
func TestLocalModelFailsAfterTwoInvalidTurns(t *testing.T) {
	e := newEnv(t)
	f := useFakeOllama(t, e)
	f.answer = func(n int) string {
		if n == 1 {
			return brokenSend
		}
		return `{"content":"","tool_calls":[{"id":"c2","function":{"name":"enviar_mensagem","arguments":"{}"}}]}`
	}
	a := e.agent(Input{Name: ptr("Local"), Enabled: ptr(true), Mode: ptr(ModeAutonomous)})
	e.receive(contact, "oi")
	run := e.lastRun(a.ID)
	if run.Status != StatusFailed || !strings.HasPrefix(run.Error, "invalid_tool_call") || !strings.Contains(run.Error, "enviar_mensagem") ||
		run.Sent != 0 || f.calls() != 2 || len(run.Steps) != 2 {
		t.Fatalf("run %+v calls=%d", run, f.calls())
	}
	if len(e.fake.WA.Sent()) != 0 {
		t.Fatal("an invalid call sent a message")
	}
}

// TestInvalidCallChecks covers the validation rules.
func TestInvalidCallChecks(t *testing.T) {
	specs := []attendant.ToolSpec{{Name: "send_message", Schema: json.RawMessage(`{"type":"object","required":["text"],
		"properties":{"text":{"type":"string"},"n":{"type":"integer"}}}`)}}
	cases := []struct {
		call attendant.ToolCall
		want string
	}{
		{attendant.ToolCall{Name: "send_message", Input: json.RawMessage(`{"text":"oi"}`)}, ""},
		{attendant.ToolCall{Name: "send_message", Input: json.RawMessage(`{}`)}, `needs the argument "text"`},
		{attendant.ToolCall{Name: "send_message", Input: json.RawMessage(`{"text":5}`)}, "must be of type string"},
		{attendant.ToolCall{Name: "send_message", Input: json.RawMessage(`{"text":"a","n":1.5}`)}, "must be of type integer"},
		{attendant.ToolCall{Name: "send_message", Malformed: true, Input: json.RawMessage(`{}`)}, "not a valid JSON object"},
		{attendant.ToolCall{Name: "", Input: json.RawMessage(`{}`)}, "available tools are: send_message"},
	}
	for _, c := range cases {
		got, bad := invalidCalls([]attendant.ToolCall{c.call}, specs)
		if (c.want == "") == bad || !strings.Contains(got[0], c.want) {
			t.Errorf("%+v: %q", c.call, got[0])
		}
	}
}
