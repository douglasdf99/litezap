package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	// t.TempDir can exceed the unix socket path limit on macOS; MkdirTemp in /tmp does not.
	d, err := os.MkdirTemp("/tmp", "lz")
	if err != nil {
		t.Skip("no /tmp:", err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestClaimInstanceSecondLaunchShowsFirst(t *testing.T) {
	dir := shortTempDir(t)
	first, err := ClaimInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := ClaimInstance(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second claim: err = %v, want ErrAlreadyRunning", err)
	}
	select {
	case <-first.Show():
	case <-time.After(3 * time.Second):
		t.Fatal("first instance was not asked to show itself")
	}

	// After the first one exits, a new launch takes over.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ClaimInstance(dir)
	if err != nil {
		t.Fatalf("claim after close: %v", err)
	}
	again.Close()
}

func TestClaimInstanceStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	// A leftover file where the socket goes (crash): nobody answers, so it is replaced.
	if err := os.WriteFile(instanceSocket(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := ClaimInstance(dir)
	if err != nil {
		t.Fatalf("claim over stale socket: %v", err)
	}
	in.Close()
}

func TestInstanceSocketLongPath(t *testing.T) {
	short := "/tmp/lz"
	if got := instanceSocket(short); got != filepath.Join(short, "app.sock") {
		t.Errorf("short dir: %q", got)
	}
	long := "/" + strings.Repeat("x", 120)
	got := instanceSocket(long)
	if len(got) > maxSocketPath+len(os.TempDir()) || !strings.HasPrefix(filepath.Base(got), "litezap-") {
		t.Errorf("long dir: %q", got)
	}
	if got != instanceSocket(long) {
		t.Error("not stable")
	}
}
