package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortTempDir is a private (0700) folder with a path short enough for a unix socket
// (t.TempDir can exceed the limit on macOS).
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket path tests")
	}
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
	fi, err := os.Stat(filepath.Join(dir, "app.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", fi.Mode().Perm())
	}

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

func TestClaimInstanceConcurrentLaunches(t *testing.T) {
	dir := shortTempDir(t)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []*Instance
		others  int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, err := ClaimInstance(dir)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, in)
			case errors.Is(err, ErrAlreadyRunning):
				others++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	for _, in := range winners {
		in.Close()
	}
	if len(winners) != 1 || others != 7 {
		t.Fatalf("winners = %d, already running = %d; want 1 and 7", len(winners), others)
	}
}

func TestClaimInstanceStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	// A leftover file where the socket goes (crash): nobody holds the lock, so it is replaced.
	if err := os.WriteFile(filepath.Join(dir, "app.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := ClaimInstance(dir)
	if err != nil {
		t.Fatalf("claim over stale socket: %v", err)
	}
	in.Close()
}

func TestInstanceCloseKeepsForeignSocket(t *testing.T) {
	dir := shortTempDir(t)
	in, err := ClaimInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "app.sock")
	// Someone replaced the socket file: Close must not delete theirs.
	os.Remove(p)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("foreign file removed: %v", err)
	}
}

func TestInstanceSocketSkipsSharedFolder(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := instanceSocket(dir)
	if err == nil && filepath.Dir(p) == dir {
		t.Fatalf("used a folder others can read: %q", p)
	}
}

func TestSocketCandidates(t *testing.T) {
	env := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}
	cache := func() (string, error) { return "/home/u/.cache", nil }
	got := socketCandidates("/home/u/.local/share/litezap", func(k string) string { return env[k] }, cache)
	if len(got) != 3 || got[0] != "/home/u/.local/share/litezap/app.sock" ||
		!strings.HasPrefix(got[1], "/run/user/1000/litezap-") || !strings.HasPrefix(got[2], "/home/u/.cache/litezap/litezap-") {
		t.Fatalf("candidates = %q", got)
	}
	for _, p := range got {
		if strings.HasPrefix(p, os.TempDir()) || strings.HasPrefix(p, "/tmp/") {
			t.Errorf("shared temp folder candidate: %q", p)
		}
	}
	// A relative XDG_RUNTIME_DIR is ignored.
	env["XDG_RUNTIME_DIR"] = "run"
	if got := socketCandidates("/d", func(k string) string { return env[k] }, cache); len(got) != 2 {
		t.Fatalf("relative XDG_RUNTIME_DIR used: %q", got)
	}
	// Same folder, same name: the hash is stable.
	a := socketCandidates("/d", func(string) string { return "" }, cache)
	b := socketCandidates("/d", func(string) string { return "" }, cache)
	if a[1] != b[1] {
		t.Fatalf("unstable name: %q vs %q", a[1], b[1])
	}
}
