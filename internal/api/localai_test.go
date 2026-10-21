package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/douglasdf99/litezap/internal/localai"
)

// TestLocalAIRoutes: /v1/ai is admin only; the Ollama address is the engine's (a fake on
// loopback here), never the request's.
func TestLocalAIRoutes(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.12.3"}`)
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"qwen2.5:7b","size":4683087332}]}`)
		case "/api/pull":
			_, _ = io.WriteString(w, "{\"status\":\"success\"}\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()
	svc, err := localai.NewAt(ollama.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc.RAM = func() uint64 { return 4 << 30 }
	e := newEnvWith(t, func(o *Options) { o.LocalAI = svc })
	_, tok := e.create("Local")
	if code, _ := e.do("GET", "/v1/ai/local", tok, ""); code != 403 {
		t.Fatalf("instance token read /v1/ai/local: %d", code)
	}
	code, body := e.do("GET", "/v1/ai/local", admin, "")
	rec, _ := body["recommended"].(map[string]any)
	if code != 200 || body["reachable"] != true || body["version"] != "0.12.3" || rec["model"] != "llama3.2:3b" {
		t.Fatalf("status = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/ai/local/pull", admin, `{"model":"qwen2.5:7b","url":"http://evil"}`); code != 400 {
		t.Fatalf("unknown field = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/ai/local/pull", admin, `{"model":"hf.co/evil/model"}`); code != 400 {
		t.Fatalf("registry host = %d %v", code, body)
	}
	if code, body := e.do("POST", "/v1/ai/local/pull", admin, `{"model":"llama3.2:3b"}`); code != 202 {
		t.Fatalf("pull = %d %v", code, body)
	}
	code, body = e.do("GET", "/v1/ai/presets", admin, "")
	if list, _ := body["presets"].([]any); code != 200 || len(list) != 3 {
		t.Fatalf("presets = %d %v", code, body)
	}
}
