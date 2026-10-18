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
	write(filepath.Join(proc, "101", "cmdline"), "/usr/bin/litezap\x00worker\x00--instance\x00inst_a\x00--socket\x00/x.sock\x00")
	// 103 is a reused PID: another process.
	write(filepath.Join(data, "run", "inst_d.pid"), "103")
	write(filepath.Join(proc, "103", "status"), "VmRSS:\t 1 kB\n")
	write(filepath.Join(proc, "103", "cmdline"), "/usr/bin/postgres\x00")
	// 102 has exited: no /proc entry.
	got := workerMemory(data, proc)
	if len(got) != 1 || got["inst_a"] != 2048*1024 {
		t.Fatalf("memory = %v", got)
	}
}
