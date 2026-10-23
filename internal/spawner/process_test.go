package spawner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
)

type connectedWA struct{}

func (connectedWA) Run(ctx context.Context, s worker.Sink) error {
	s.ConnState("connected", "", "x@s.whatsapp.net")
	return nil
}
func (connectedWA) PairPhone(context.Context, string) (string, error) { return "", nil }
func (connectedWA) Logout(context.Context) error                      { return nil }
func (connectedWA) Disconnect()                                       {}

func TestMain(m *testing.M) {
	switch os.Getenv("LITEZAP_TEST_WORKER") {
	case "serve":
		lis, err := net.Listen("unix", os.Getenv("LITEZAP_TEST_SOCKET"))
		if err != nil {
			os.Exit(3)
		}
		srv := worker.NewServer(connectedWA{})
		srv.Start(context.Background())
		g := grpc.NewServer()
		workerv1.RegisterWorkerServer(g, srv)
		go g.Serve(lis)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		eof := make(chan struct{})
		go func() { // parent-liveness pipe: EOF means the supervisor is gone
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(eof)
		}()
		select {
		case <-sig:
		case <-eof:
		}
		g.Stop()
		os.Exit(0)
	case "chatty": // prints to both streams and exits
		fmt.Fprintln(os.Stdout, "to stdout")
		fmt.Fprintln(os.Stderr, "to stderr")
		os.Exit(1)
	case "exit":
		os.Exit(1)
	case "idle": // lives until its stdin closes
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testSpawner(t *testing.T, mode string) *Process {
	return &Process{
		DataDir:      t.TempDir(),
		StartTimeout: 5 * time.Second,
		Log:          slog.New(slog.DiscardHandler),
		Command: func(id, sock string) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "LITEZAP_TEST_WORKER="+mode, "LITEZAP_TEST_SOCKET="+sock)
			return cmd
		},
	}
}

func TestProcessStartPingClose(t *testing.T) {
	p := testSpawner(t, "serve")
	ctx := context.Background()
	h, err := p.Start(ctx, "inst_a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Kill() })
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := h.Client().Ping(pctx, &workerv1.PingRequest{})
	if err != nil || resp.State != "connected" {
		t.Fatalf("ping = %v, %v", resp, err)
	}
	if _, err := os.Stat(p.pidFile("inst_a")); err != nil {
		t.Fatalf("pid file missing: %v", err)
	}
	if err := h.Close(pctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit")
	}
}

func TestProcessKill(t *testing.T) {
	p := testSpawner(t, "serve")
	h, err := p.Start(context.Background(), "inst_b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Kill() })
	if err := h.Kill(); err != nil {
		t.Fatal(err)
	}
	<-h.Done()
}

func TestProcessStartFailsWhenWorkerExits(t *testing.T) {
	p := testSpawner(t, "exit")
	_, err := p.Start(context.Background(), "inst_c")
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected exit error, got %v", err)
	}
}

func TestProcessWorkerExitsWhenParentPipeCloses(t *testing.T) {
	p := testSpawner(t, "serve")
	h, err := p.Start(context.Background(), "inst_d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Kill() })
	// Simulate the supervisor dying: its end of the liveness pipe goes away.
	_ = h.(*procHandle).stdin.Close()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after parent pipe closed")
	}
}

func TestIsWorkerProcessMatchesExactInstanceID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan detection is not supported on Windows")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$", "worker",
		"--instance", "inst_ab", "--socket", "/tmp/x.sock", "--data-dir", dir)
	cmd.Env = append(os.Environ(), "LITEZAP_TEST_WORKER=idle")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })

	pid := cmd.Process.Pid
	if !isWorkerProcess(pid, "inst_ab", dir) {
		t.Fatal("exact instance id not matched")
	}
	if isWorkerProcess(pid, "inst_a", dir) {
		t.Fatal("instance id prefix must not match a longer id")
	}
	if isWorkerProcess(pid, "inst_ab", dir[:len(dir)-1]) {
		t.Fatal("data dir prefix must not match a longer data dir")
	}
}

func TestProcessEnvironAddsInstanceEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://leak:3128")
	t.Setenv("all_proxy", "socks5://leak:1080")
	p := &Process{MemoryLimitMB: 64, Env: func(_ context.Context, id string) ([]string, error) {
		return []string{"LITEZAP_WORKER_PROXY_URL=socks5://" + id + ":1080"}, nil
	}}
	env, err := p.environ(context.Background(), exec.Command("true"), "inst_e")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GOMEMLIMIT=64MiB") || !strings.Contains(joined, "LITEZAP_WORKER_PROXY_URL=socks5://inst_e:1080") {
		t.Fatalf("env misses entries: %v", env[len(env)-2:])
	}
	if strings.Contains(joined, "leak") {
		t.Fatal("proxy variables of the supervisor leaked into the worker environment")
	}
	p.Env = func(context.Context, string) ([]string, error) { return nil, io.ErrUnexpectedEOF }
	if _, err := p.environ(context.Background(), exec.Command("true"), "inst_e"); err == nil {
		t.Fatal("expected env error")
	}
}

func TestProcessEnvironStripsInheritedProxySettings(t *testing.T) {
	t.Setenv("LITEZAP_WORKER_PROXY_URL", "socks5://stale:1080")
	t.Setenv("Https_Proxy", "http://mixed-case:3128") // Windows environment names are case-insensitive
	p := &Process{}
	env, err := p.environ(context.Background(), exec.Command("true"), "inst_e")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "stale") || strings.Contains(joined, "mixed-case") {
		t.Fatal("inherited proxy settings leaked into the worker environment")
	}
}
