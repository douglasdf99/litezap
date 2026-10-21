package attendant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFreePresets: a preset fills the openai provider's server and model, Ollama needs
// no key, the cloud presets ask for their free key, and free calls cost nothing.
func TestFreePresets(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	v, err := e.ad.PutSettings(ctx, inst, SettingsPatch{Preset: ptr(PresetOllama)})
	if err != nil {
		t.Fatal(err)
	}
	if v.Provider != ProviderOpenAI || v.BaseURL != OllamaBaseURL || v.Model != "qwen2.5:7b" || !v.Ready || !v.Free || v.APIKeySet {
		t.Fatalf("ollama preset %+v", v)
	}
	// Another model of the preset is kept; the cost of a call is 0.
	if v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{Model: ptr("llama3.2:3b")}); err != nil || v.Model != "llama3.2:3b" || v.Preset != PresetOllama {
		t.Fatalf("model change %+v %v", v, err)
	}
	s, _, _, _ := e.att.load(ctx, inst)
	if c := s.costMicros("llama3.2:3b", 1_000_000, 1_000_000); c != 0 {
		t.Fatalf("local cost %d", c)
	}

	// "provider": "gemini_free" is the preset; without its key it is not ready.
	v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{Provider: ptr(PresetGeminiFree)})
	if err != nil {
		t.Fatal(err)
	}
	if v.Preset != PresetGeminiFree || v.Provider != ProviderOpenAI || v.Model != "gemini-2.5-flash" ||
		!strings.HasPrefix(v.BaseURL, "https://generativelanguage.googleapis.com/") || v.Ready ||
		!strings.Contains(v.NotReady, "aistudio.google.com") || !v.Free {
		t.Fatalf("gemini preset %+v", v)
	}
	if v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{APIKey: ptr("AIza-free-key")}); err != nil || !v.Ready {
		t.Fatalf("gemini with key %+v %v", v, err)
	}

	v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{Preset: ptr(PresetGroqFree), APIKey: ptr("gsk_free")})
	if err != nil || v.Model != "llama-3.3-70b-versatile" || v.BaseURL != "https://api.groq.com/openai/v1" || !v.Ready || !v.Free {
		t.Fatalf("groq preset %+v %v", v, err)
	}
	// A preset label on a server that is not the preset's is not free.
	if v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{BaseURL: ptr("https://api.openai.com/v1")}); err != nil || v.Free {
		t.Fatalf("custom server reported free %+v %v", v, err)
	}
	// Anthropic clears the preset; unknown presets are refused.
	if v, err = e.ad.PutSettings(ctx, inst, SettingsPatch{Provider: ptr(ProviderAnthropic), APIKey: ptr("sk-ant-x")}); err != nil ||
		v.Preset != "" || v.Model != DefaultModel || v.Free || v.BaseURL != "" {
		t.Fatalf("anthropic %+v %v", v, err)
	}
	if _, err := e.ad.PutSettings(ctx, inst, SettingsPatch{Preset: ptr("chatgpt_free")}); err == nil {
		t.Fatal("unknown preset accepted")
	}
	if _, err := e.ad.PutSettings(ctx, inst, SettingsPatch{Preset: ptr(PresetOllama), Provider: ptr(ProviderAnthropic)}); err == nil {
		t.Fatal("preset with anthropic accepted")
	}
	if len(ProviderPresets()) != 3 {
		t.Fatal("presets")
	}
}

// TestProviderChangeDropsOldKey: a key of one provider never goes to another.
func TestProviderChangeDropsOldKey(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	if _, err := e.ad.PutSettings(ctx, inst, SettingsPatch{Preset: ptr(PresetGeminiFree), APIKey: ptr("AIza-free")}); err != nil {
		t.Fatal(err)
	}
	v, err := e.ad.PutSettings(ctx, inst, SettingsPatch{Provider: ptr(ProviderAnthropic)})
	if err != nil || v.APIKeySet || v.Ready || v.BaseURL != "" {
		t.Fatalf("anthropic kept the gemini key or URL: %+v %v", v, err)
	}
}

// TestGeminiOmitsUser:Gemini's OpenAI endpoint does not get the "user" field.
func TestGeminiOmitsUser(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"{\"reply\":\"oi\"}"}}]}`)
	}))
	defer srv.Close()
	s := Settings{Provider: ProviderOpenAI, Preset: PresetGeminiFree, BaseURL: srv.URL, Model: "gemini-2.5-flash"}
	b, _ := DefaultFactory(s, Secrets{APIKey: "k"})
	if _, err := b.Chat.Chat(context.Background(), ChatRequest{System: "s", User: "lz_1", MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["user"]; !ok {
		t.Fatal("a server that is not Gemini lost the user field")
	}
	s.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	if s.sendsUser() {
		t.Fatal("gemini would get the user field")
	}
}

// TestMessyToolCalls: what small local models send instead of clean tool_calls.
func TestMessyToolCalls(t *testing.T) {
	answers := []string{
		// arguments as an object instead of a JSON string
		`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"send_message","arguments":{"text":"oi"}}}]}}]}`,
		// fenced arguments
		`{"choices":[{"message":{"tool_calls":[{"function":{"name":"send_message","arguments":"` + "```json\\n{\\\"text\\\":\\\"oi\\\"}\\n```" + `"}}]}}]}`,
		// broken JSON arguments
		`{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"send_message","arguments":"{\"text\": \"oi"}}]}}]}`,
		// Qwen/Hermes style call written in the text
		`{"choices":[{"message":{"content":"Vou responder.\n<tool_call>\n{\"name\": \"send_message\", \"arguments\": {\"text\": \"oi\"}}\n</tool_call>"}}]}`,
		// Llama style: the whole answer is the call
		`{"choices":[{"message":{"content":"{\"name\": \"send_message\", \"parameters\": {\"text\": \"oi\"}}"}}]}`,
		// JSON text naming a tool that was not offered stays text
		`{"choices":[{"message":{"content":"{\"name\": \"rm_rf\", \"parameters\": {}}"}}]}`,
	}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, answers[n])
		n++
	}))
	defer srv.Close()
	c := &openAIChat{base: srv.URL, model: "m", client: httpClient()}
	req := ToolChatRequest{System: "s", MaxTokens: 100, Tools: []ToolSpec{{Name: "send_message", Schema: testToolSchema}},
		Turns: []ToolTurn{{Role: "user", Text: "oi"}}}
	get := func() ToolChatResponse {
		t.Helper()
		r, err := c.ChatTools(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	clean := func(r ToolChatResponse, what string) {
		t.Helper()
		if len(r.Turn.Calls) != 1 || r.Turn.Calls[0].Malformed || r.Turn.Calls[0].Name != "send_message" ||
			string(r.Turn.Calls[0].Input) != `{"text":"oi"}` && string(r.Turn.Calls[0].Input) != `{"text": "oi"}` || r.StopReason != "tool_use" {
			t.Fatalf("%s: %+v", what, r)
		}
	}
	clean(get(), "object arguments")
	clean(get(), "fenced arguments")
	if r := get(); len(r.Turn.Calls) != 1 || !r.Turn.Calls[0].Malformed || string(r.Turn.Calls[0].Input) != `{}` ||
		!strings.Contains(r.Turn.Calls[0].Raw, `"text": "oi`) {
		t.Fatalf("broken arguments %+v", r)
	}
	r := get()
	clean(r, "tool_call tag")
	if r.Turn.Text != "Vou responder." {
		t.Fatalf("text around the tag %q", r.Turn.Text)
	}
	clean(get(), "whole-answer call")
	if r := get(); len(r.Turn.Calls) != 0 || r.StopReason != "end_turn" || !strings.Contains(r.Turn.Text, "rm_rf") {
		t.Fatalf("unoffered tool %+v", r)
	}
}
