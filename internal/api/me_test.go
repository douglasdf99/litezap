package api

import (
	"net/http"
	"sync"
	"testing"

	"github.com/douglasdf99/litezap/internal/service"
)

func TestMe(t *testing.T) {
	e := newEnv(t)
	if code, body := e.do("GET", "/v1/me", admin, ""); code != 200 || body["admin"] != true || body["instance_id"] != nil {
		t.Fatalf("me admin = %d %v", code, body)
	}
	id, tok := e.create("Me")
	if code, body := e.do("GET", "/v1/me", tok, ""); code != 200 || body["admin"] != false || body["instance_id"] != id {
		t.Fatalf("me instance = %d %v", code, body)
	}
	if code, _ := e.do("GET", "/v1/me", "", ""); code != 401 {
		t.Fatalf("me without token = %d", code)
	}
}

func TestMCPMountIsAuthenticatedAndScoped(t *testing.T) {
	var mu sync.Mutex
	var got []service.Scope
	e := newEnvWith(t, func(o *Options) {
		o.MCP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc, ok := service.ScopeFrom(r.Context())
			if !ok {
				t.Error("no scope in the MCP request context")
			}
			mu.Lock()
			got = append(got, sc)
			mu.Unlock()
			w.WriteHeader(http.StatusTeapot)
		})
	})
	if code, body := e.do("POST", "/mcp", "", "{}"); code != 401 || errCode(body) != "unauthorized" {
		t.Fatalf("mcp without token = %d %v", code, body)
	}
	if code, _ := e.do("POST", "/mcp", "lzi_wrong", "{}"); code != 401 {
		t.Fatalf("mcp with bad token = %d", code)
	}
	id, tok := e.create("M")
	if code, _ := e.do("POST", "/mcp", admin, "{}"); code != http.StatusTeapot {
		t.Fatalf("mcp admin = %d", code)
	}
	if code, _ := e.do("POST", "/mcp", tok, "{}"); code != http.StatusTeapot {
		t.Fatalf("mcp instance = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !got[0].Admin || got[1].Admin || got[1].InstanceID != id {
		t.Fatalf("scopes = %+v", got)
	}
}
