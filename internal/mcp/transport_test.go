package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/service"
)

func TestHTTPHostCheck(t *testing.T) {
	e := newEnv(t, nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for _, c := range []struct {
		publicHost, host string
		want             int
	}{
		{"", "evil.example", http.StatusForbidden},
		{"", "localhost", http.StatusOK},
		{"zap.example.com", "zap.example.com", http.StatusOK},
		{"zap.example.com", "evil.example", http.StatusForbidden},
		{"zap.example.com", "127.0.0.1", http.StatusOK},
	} {
		h := NewHTTPHandler(HTTPDeps{Instances: e.svc, Messaging: e.msg, Events: e.bus, PublicHost: c.publicHost})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(service.WithScope(r.Context(), service.Scope{Admin: true})))
		}))
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		req.Host = c.host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if resp.StatusCode != c.want {
			t.Errorf("public %q host %q = %d, want %d", c.publicHost, c.host, resp.StatusCode, c.want)
		}
	}
}
