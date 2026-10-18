package serve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// require_proxy is also enforced where the worker is spawned: never a direct connection.
func TestWorkerEnvRefusesDirectWhenProxyRequired(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateInstance(ctx, store.Instance{ID: "i", Name: "i", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RequireProxy = true
	if _, err := workerEnv(st, cfg)(ctx, "i"); !errors.Is(err, errProxyRequired) {
		t.Fatalf("env without proxy = %v", err)
	}
	cfg.DefaultProxyURL = "socks5://p:1080"
	if got, err := workerEnv(st, cfg)(ctx, "i"); err != nil || got[0] != "LITEZAP_WORKER_PROXY_URL=socks5://p:1080" {
		t.Fatalf("env with default proxy = %v, %v", got, err)
	}
}

func TestShutdownScale(t *testing.T) {
	for in, want := range map[time.Duration]float64{0: 1, time.Minute: 1, 20 * time.Second: 0.5} {
		if got := shutdownScale(in); got != want {
			t.Errorf("shutdownScale(%v) = %v, want %v", in, got, want)
		}
	}
}

// ops_listen_addr serves health, readiness and metrics without a token.
func TestOpsListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opsAddr := probe.Addr().String()
	probe.Close()
	t.Setenv("LITEZAP_OPS_LISTEN_ADDR", opsAddr)
	info := startServe(t, Options{})
	for path, want := range map[string]string{"/healthz": `"ok"`, "/readyz": `"ready"`, "/metrics": "litezap_up 1"} {
		resp, err := http.Get("http://" + opsAddr + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Errorf("%s = %d %s", path, resp.StatusCode, b)
		}
	}
	// The API listener (loopback here) serves /metrics too.
	resp, err := http.Get(info.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("api /metrics = %d", resp.StatusCode)
	}
}
