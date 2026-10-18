package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"localhost:8787": true,
		"[::1]:8787":     true,
		"0.0.0.0:8787":   false,
		":8787":          false,
		"10.0.0.5:8787":  false,
		"garbage":        false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// A second supervisor on a busy address must fail before CleanupOrphans touches the
// first supervisor's workers (CleanupOrphans removes every run/*.pid it visits).
func TestRunFailsToBindBeforeTouchingWorkers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dir := t.TempDir()
	pid := filepath.Join(dir, "run", "inst_x.pid")
	if err := os.MkdirAll(filepath.Dir(pid), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pid, []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITEZAP_DATA_DIR", dir)
	t.Setenv("LITEZAP_LISTEN_ADDR", ln.Addr().String())
	if err := Run(context.Background(), "", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Run succeeded on a busy address")
	}
	if _, err := os.Stat(pid); err != nil {
		t.Fatalf("worker pid file touched before bind failed: %v", err)
	}
}

func TestWorkerEnvPassesEffectiveProxy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for id, js := range map[string]string{"own": `{"proxy_url":"http://own:3128"}`, "dflt": "{}"} {
		if err := st.CreateInstance(ctx, store.Instance{ID: id, Name: id, State: instance.Created, TokenHash: id, SettingsJSON: js}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.DefaultProxyURL = "socks5://default:1080"
	env := workerEnv(st, cfg)
	for id, want := range map[string]string{"own": "http://own:3128", "dflt": "socks5://default:1080"} {
		got, err := env(ctx, id)
		if err != nil || len(got) != 1 || got[0] != "LITEZAP_WORKER_PROXY_URL="+want {
			t.Errorf("%s: env = %v, %v", id, got, err)
		}
	}
	cfg.DefaultProxyURL = ""
	if got, _ := workerEnv(st, cfg)(ctx, "dflt"); got[0] != "LITEZAP_WORKER_PROXY_URL=" {
		t.Errorf("no proxy: env = %v", got)
	}
	if _, err := env(ctx, "missing"); err == nil {
		t.Error("expected error for unknown instance")
	}
}

// A request still running when the graceful shutdown gives up is cut (srv.Close) before the
// queues, workers and store go away.
func TestShutdownClosesHungRequests(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	dir := t.TempDir()
	t.Setenv("LITEZAP_DATA_DIR", dir)
	t.Setenv("LITEZAP_LISTEN_ADDR", addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "", slog.New(slog.DiscardHandler)) }()
	var conn net.Conn
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("server did not start: %v", err)
	}
	defer conn.Close()
	tok, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatal(err)
	}
	// A send whose body never finishes arriving: the handler blocks reading it.
	fmt.Fprintf(conn, "POST /v1/instances/inst_x/messages HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\nContent-Length: 1000000\r\n\r\n{\"to\":", strings.TrimSpace(string(tok)))
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("the hung request's connection is still open after Run returned")
		}
	}
}

// An open SSE stream ends at shutdown instead of holding Run for the whole grace period.
func TestShutdownEndsSSEStreams(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	dir := t.TempDir()
	t.Setenv("LITEZAP_DATA_DIR", dir)
	t.Setenv("LITEZAP_LISTEN_ADDR", addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "", slog.New(slog.DiscardHandler)) }()
	var tok []byte
	for i := 0; i < 100; i++ {
		if tok, err = os.ReadFile(filepath.Join(dir, "admin_token")); err == nil {
			if c, err := net.Dial("tcp", addr); err == nil {
				c.Close()
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("sse = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("shutdown took %v: the SSE stream held it", d)
	}
}
