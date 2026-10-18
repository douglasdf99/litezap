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
	"github.com/douglasdf99/litezap/internal/dashboard"
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

func startServe(t *testing.T, opt Options) Info {
	t.Helper()
	t.Setenv("LITEZAP_DATA_DIR", t.TempDir())
	t.Setenv("LITEZAP_LISTEN_ADDR", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	infoc := make(chan Info, 1)
	opt.OnListening = func(i Info) { infoc <- i }
	done := make(chan error, 1)
	go func() { done <- RunWith(ctx, opt, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("Run did not return")
		}
	})
	select {
	case i := <-infoc:
		return i
	case err := <-done:
		t.Fatalf("serve exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not start")
	}
	return Info{}
}

func TestDashboardMountedWithOneClickLogin(t *testing.T) {
	codes := dashboard.NewLaunchCodes()
	info := startServe(t, Options{LaunchCodes: codes})
	if !strings.HasPrefix(info.URL, "http://127.0.0.1:") || !info.Dashboard || info.AdminToken == "" {
		t.Fatalf("info = %+v", info)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(info.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/app/" {
		t.Fatalf("/ = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = http.Get(info.URL + "/app/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "LiteZap") {
		t.Fatalf("/app/ = %d", resp.StatusCode)
	}
	// The API keeps working behind the dashboard mux.
	resp, err = http.Get(info.URL + "/v1/instances")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1/instances without token = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", info.URL+"/app/api/session", strings.NewReader(`{"code":"`+codes.Mint()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", info.URL)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), info.AdminToken) {
		t.Fatalf("session exchange = %d %s", resp.StatusCode, body)
	}
}

func TestDashboardCanBeDisabled(t *testing.T) {
	t.Setenv("LITEZAP_DISABLE_DASHBOARD", "true")
	info := startServe(t, Options{LaunchCodes: dashboard.NewLaunchCodes()})
	if info.Dashboard {
		t.Fatal("dashboard reported as enabled")
	}
	for _, p := range []string{"/app/", "/app/api/info"} {
		resp, err := http.Get(info.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s = %d with the dashboard disabled", p, resp.StatusCode)
		}
	}
}

func TestDashboardURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:8787": "http://127.0.0.1:8787",
		"0.0.0.0:8787":   "http://127.0.0.1:8787",
		"[::]:8787":      "http://127.0.0.1:8787",
		"[::1]:8787":     "http://[::1]:8787",
	} {
		if got := dashboardURL(in); got != want {
			t.Errorf("dashboardURL(%q) = %q, want %q", in, got, want)
		}
	}
}
