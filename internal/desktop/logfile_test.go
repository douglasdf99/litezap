package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileRotatesWhileRunning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.log")
	l, err := OpenLog(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for i := 0; i < 5; i++ {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.ReadFile(p)
	old, _ := os.ReadFile(p + ".1")
	// 2 lines fit in 100 bytes: the 3rd started a new file, the 5th another one.
	if len(cur) != 40 || len(old) != 80 {
		t.Fatalf("current = %d bytes, old = %d bytes", len(cur), len(old))
	}
}

func TestOpenLogRotatesAnOversizedLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.log")
	if err := os.WriteFile(p, []byte(strings.Repeat("y", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	l.Write([]byte("new\n"))
	l.Close()
	cur, _ := os.ReadFile(p)
	if string(cur) != "new\n" {
		t.Fatalf("current = %q", cur)
	}
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != 200 {
		t.Fatalf("old copy: %v", err)
	}
}
