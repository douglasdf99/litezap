package serve

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
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
