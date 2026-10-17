package spawner

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
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
		<-sig
		g.Stop()
		os.Exit(0)
	case "exit":
		os.Exit(1)
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
