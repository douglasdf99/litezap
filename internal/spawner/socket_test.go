package spawner

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSocketPathIsShortAndStable(t *testing.T) {
	long := "/Users/someone/Library/Application Support/LiteZap/a/very/long/data/directory/that/keeps/going"
	a := SocketPath(long, "inst_0123456789abcdef")
	if len(a) >= 100 {
		t.Fatalf("socket path too long (%d): %s", len(a), a)
	}
	if a != SocketPath(long, "inst_0123456789abcdef") {
		t.Fatal("not deterministic")
	}
	if a == SocketPath(long, "inst_other") || a == SocketPath("/other", "inst_0123456789abcdef") {
		t.Fatal("collision between instances or data dirs")
	}
}

func TestEnsureSocketDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	base := t.TempDir()
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketDir(filepath.Join(loose, "a.sock")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(loose); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketDir(filepath.Join(link, "a.sock")); err == nil {
		t.Fatal("symlinked socket dir must be rejected")
	}
}
