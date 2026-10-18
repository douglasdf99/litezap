package spawner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerMemoryFromProc(t *testing.T) {
	data, proc := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "run", "inst_a.pid"), "101\n")
	write(filepath.Join(data, "run", "inst_b.pid"), "102")
	write(filepath.Join(data, "run", "inst_c.pid"), "garbage")
	write(filepath.Join(proc, "101", "status"), "Name:\tlitezap\nVmPeak:\t 9999 kB\nVmRSS:\t   2048 kB\n")
	// 102 has exited: no /proc entry.
	got := workerMemory(data, proc)
	if len(got) != 1 || got["inst_a"] != 2048*1024 {
		t.Fatalf("memory = %v", got)
	}
}
