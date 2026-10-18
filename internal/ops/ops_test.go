package ops

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/usage"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	b, _ := io.ReadAll(rr.Body)
	return rr.Code, string(b)
}

func TestHealthAndReadiness(t *testing.T) {
	ping := error(nil)
	h := &Health{Ping: func(context.Context) error { return ping }}
	srv := Handler(h, nil)
	if code, _ := get(t, srv, "/healthz"); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, body := get(t, srv, "/readyz"); code != 200 || !strings.Contains(body, "ready") {
		t.Fatalf("readyz = %d %s", code, body)
	}
	ping = errors.New("db down")
	if code, body := get(t, srv, "/readyz"); code != 503 || !strings.Contains(body, "unavailable") {
		t.Fatalf("readyz with store down = %d %s", code, body)
	}
	ping = nil
	h.Drain()
	if code, body := get(t, srv, "/readyz"); code != 503 || !strings.Contains(body, "draining") {
		t.Fatalf("readyz while draining = %d %s", code, body)
	}
	if code, _ := get(t, srv, "/healthz"); code != 200 {
		t.Fatal("healthz must stay up while draining")
	}
	if code, _ := get(t, srv, "/metrics"); code != 404 {
		t.Fatal("metrics without a handler")
	}
}

func TestMetricsExposition(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b"} {
		if err := st.CreateInstance(ctx, store.Instance{ID: id, Name: id, State: instance.Connected, TokenHash: id}); err != nil {
			t.Fatal(err)
		}
	}
	rec := &usage.Recorder{Store: st}
	bus := events.NewBus(nil, rec)
	bus.Publish("a", events.TypeMessageSent, events.Message{Source: "api", Type: "text"})
	bus.Publish("a", events.TypeInstanceStatus, events.InstanceStatus{State: instance.Starting})
	pw := &usage.ProxyWatch{}
	pw.StateChanged("b", instance.Reconnecting, usage.ProxyUnreachable)
	sending := &service.Sending{Store: st}
	_ = sending.SetInstance(ctx, "a", true, "billing")
	h := &Health{Ping: st.Ping}
	code, body := get(t, Handler(h, Metrics(Source{Store: st, Usage: rec, Proxy: pw, Health: h, Sending: sending, Version: "v1"})), "/metrics")
	if code != 200 {
		t.Fatalf("metrics = %d", code)
	}
	for _, want := range []string{
		"# TYPE litezap_instances gauge",
		`litezap_instances{state="connected"} 2`,
		`litezap_instances{state="banned"} 0`,
		`litezap_queue_messages{status="queued"} 0`,
		`litezap_webhook_deliveries{status="failed"} 0`,
		"litezap_messages_sent_total 1",
		`litezap_instance_state_changes_total{state="starting"} 1`,
		"litezap_proxy_failures_total 1",
		"litezap_proxy_unreachable_instances 1",
		"litezap_sending_suspended 0",
		"litezap_sending_suspended_instances 1",
		"litezap_ready 1",
		"litezap_draining 0",
		`litezap_build_info{version="v1"} 1`,
		"go_goroutines ",
	} {
		if !strings.Contains(body, want+"\n") && !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
