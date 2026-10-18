package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttendantRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.create("Att")
	base := "/v1/instances/" + id
	// A fake OpenAI-compatible server (no real LLM calls).
	var auth string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"fake","choices":[{"message":{"content":"{\"reply\":\"Olá, sou a Bia!\",\"handoff\":false,\"handoff_note\":\"\",\"event\":{\"type\":\"none\",\"customer_name\":\"\",\"need\":\"\",\"preferred_time\":\"\",\"items\":[],\"address\":\"\",\"notes\":\"\"}}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer llm.Close()

	code, body := e.do("GET", base+"/attendant", tok, "")
	if code != 200 || body["configured"] != false || body["provider"] != "anthropic" || body["model"] != "claude-sonnet-5-5" {
		t.Fatalf("get default = %d %v", code, body)
	}
	cfg := `{"provider":"openai","base_url":"` + llm.URL + `","model":"fake","api_key":"sk-fake-key-123456"}`
	// The provider and its key are admin-only.
	if code, _ := e.do("PUT", base+"/attendant", tok, cfg); code != 403 {
		t.Fatalf("instance token set the provider: %d", code)
	}
	if code, body := e.do("PUT", base+"/attendant", admin, `{"provider":"openai","bogus":1}`); code != 400 {
		t.Fatalf("unknown field = %d %v", code, body)
	}
	code, body = e.do("PUT", base+"/attendant", admin, cfg)
	if code != 200 || body["api_key_set"] != true || body["ready"] != true || body["api_key"] != nil {
		t.Fatalf("put = %d %v", code, body)
	}
	if _, raw := e.raw("GET", base+"/attendant", tok); strings.Contains(raw, "sk-fake-key") {
		t.Fatal("GET returned the key")
	}
	code, body = e.do("GET", base+"/attendant/presets", tok, "")
	if presets, _ := body["presets"].(map[string]any); code != 200 || len(presets) != 5 || presets["sdr"] == nil {
		t.Fatalf("presets = %d %v", code, body)
	}
	if code, body := e.do("POST", base+"/attendant/agents", tok, `{"name":"Bia","mode":"robo"}`); code != 400 {
		t.Fatalf("bad mode = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/attendant/agents", tok, `{"name":"Bia","mode":"vendedor","business":"Loja X",
		"knowledge":[{"kind":"product","title":"Camiseta","price":49.9}]}`)
	if code != 201 || body["active"] != true || body["objective"] == "" {
		t.Fatalf("create = %d %v", code, body)
	}
	agentID := body["id"].(string)
	code, body = e.do("PUT", base+"/attendant/agents/"+agentID, tok, `{"name":"Bia 2","mode":"vendedor","call_followup":true}`)
	if code != 200 || body["name"] != "Bia 2" || body["call_followup"] != true {
		t.Fatalf("update = %d %v", code, body)
	}
	code, body = e.do("POST", base+"/attendant/simulate", tok, `{"messages":[{"from":"automatic","text":"Não atendemos ligações"},{"text":"oi"}]}`)
	if code != 200 || body["reply"] != "Olá, sou a Bia!" || auth != "Bearer sk-fake-key-123456" {
		t.Fatalf("simulate = %d %v auth=%q", code, body, auth)
	}
	code, body = e.do("GET", base+"/attendant/usage?days=3", tok, "")
	if u, _ := body["usage"].([]any); code != 200 || len(u) != 1 || u[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("usage = %d %v", code, body)
	}
	// Another instance never reaches this instance's agents.
	id2, tok2 := e.create("Other")
	if code, _ := e.do("GET", base+"/attendant/agents", tok2, ""); code != 403 {
		t.Fatalf("foreign token = %d", code)
	}
	if code, _ := e.do("GET", "/v1/instances/"+id2+"/attendant/agents/"+agentID, tok2, ""); code != 404 {
		t.Fatalf("foreign agent = %d", code)
	}
	// API keys (scoped) cannot use the attendant routes.
	_, out := e.do("POST", base+"/api-keys", tok, `{"name":"k","scopes":["read","send"]}`)
	key := out["key"].(string)
	if code, _ := e.do("GET", base+"/attendant", key, ""); code != 403 {
		t.Fatalf("api key read the attendant: %d", code)
	}
	if code, _ := e.do("POST", base+"/attendant/simulate", key, `{"messages":[{"text":"oi"}]}`); code != 403 {
		t.Fatalf("api key simulated: %d", code)
	}
	if code, _ := e.do("POST", base+"/attendant/agents/"+agentID+"/activate", tok, ""); code != 200 {
		t.Fatalf("activate = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/attendant/agents/"+agentID, tok, ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/attendant/agents/"+agentID, tok, ""); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
	// The hook accepts the builtin target without a URL.
	code, body = e.do("PUT", base+"/agent-hook", tok, `{"target":"builtin","enabled":true}`)
	if code != 200 || body["target"] != "builtin" {
		t.Fatalf("builtin target = %d %v", code, body)
	}
}

// raw returns the status and the raw body.
func (e *env) raw(method, path, tok string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
