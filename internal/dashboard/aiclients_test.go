package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/desktop"
)

type fakeAI struct {
	calls []string
	err   error
}

func (f *fakeAI) Statuses() []desktop.AIStatus {
	return []desktop.AIStatus{{ID: desktop.ClaudeDesktop, Name: "Claude Desktop", Connected: true}}
}

func (f *fakeAI) Connect(_ context.Context, id string) (desktop.AIResult, error) {
	f.calls = append(f.calls, "connect "+id)
	return desktop.AIResult{Changed: true}, f.err
}

func (f *fakeAI) Disconnect(_ context.Context, id string) (desktop.AIResult, error) {
	f.calls = append(f.calls, "disconnect "+id)
	return desktop.AIResult{Changed: true}, f.err
}

func aiRequest(method, path, token string, mod func(*http.Request)) *http.Request {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	r.Host = "127.0.0.1:8787"
	r.RemoteAddr = "127.0.0.1:50000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:8787")
	}
	if mod != nil {
		mod(r)
	}
	return r
}

func TestAIClientsEndpoints(t *testing.T) {
	ai := &fakeAI{}
	h := Handler(Options{AdminToken: "adm", AIClients: ai})
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := do(aiRequest("GET", "/app/api/info", "", nil))
	var info map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &info)
	if !info["ai_clients"] {
		t.Errorf("info = %s", rec.Body)
	}

	rec = do(aiRequest("GET", "/app/api/ai-clients", "adm", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = do(aiRequest("POST", "/app/api/ai-clients/claude-desktop/connect", "adm", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Reinicie o Claude Desktop") {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	rec = do(aiRequest("POST", "/app/api/ai-clients/cursor/disconnect", "adm", nil))
	if rec.Code != 200 {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
	if fmt.Sprint(ai.calls) != "[connect claude-desktop disconnect cursor]" {
		t.Errorf("calls = %v", ai.calls)
	}

	// Refusals: none of them reaches the connector.
	ai.calls = nil
	for name, r := range map[string]*http.Request{
		"no token":      aiRequest("POST", "/app/api/ai-clients/cursor/connect", "", nil),
		"wrong token":   aiRequest("POST", "/app/api/ai-clients/cursor/connect", "nope", nil),
		"remote client": aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:5000" }),
		"rebinding":     aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", func(r *http.Request) { r.Host = "evil.example:8787" }),
		"cross origin":  aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }),
		"form body":     aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }),
		"unknown":       aiRequest("POST", "/app/api/ai-clients/chatgpt/connect", "adm", nil),
		"bad action":    aiRequest("POST", "/app/api/ai-clients/cursor/explode", "adm", nil),
	} {
		if rec := do(r); rec.Code < 400 {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if len(ai.calls) != 0 {
		t.Errorf("refused requests reached the connector: %v", ai.calls)
	}

	ai.err = fmt.Errorf("x: %w", desktop.ErrMalformedConfig)
	rec = do(aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "config_malformed") {
		t.Errorf("malformed: %d %s", rec.Code, rec.Body)
	}
}

func TestAIClientsDisabledByDefault(t *testing.T) {
	h := Handler(Options{AdminToken: "adm"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, aiRequest("POST", "/app/api/ai-clients/cursor/connect", "adm", nil))
	if rec.Code == 200 {
		t.Fatal("endpoint enabled without a connector")
	}
}
