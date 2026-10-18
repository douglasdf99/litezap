package spawner

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// WorkerMemory returns the resident memory (bytes) of each running worker, by instance
// ID, read from the pid files and /proc. It is Linux only (nil elsewhere): other systems
// have no cheap way to read another process's memory.
func WorkerMemory(dataDir string) map[string]int64 {
	if runtime.GOOS != "linux" {
		return nil
	}
	return workerMemory(dataDir, "/proc")
}

func workerMemory(dataDir, proc string) map[string]int64 {
	files, _ := filepath.Glob(filepath.Join(dataDir, "run", "*.pid"))
	out := map[string]int64{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".pid")
		dir := filepath.Join(proc, strconv.Itoa(pid))
		// A stale pid file may name a reused PID: only count our worker of this instance.
		if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err != nil ||
			!strings.Contains(string(cmd), "\x00worker\x00") || !strings.Contains(string(cmd), "\x00--instance\x00"+id+"\x00") {
			continue
		}
		if rss, ok := vmRSS(filepath.Join(dir, "status")); ok {
			out[id] = rss
		}
	}
	return out
}

// vmRSS reads "VmRSS: <n> kB" from a /proc/<pid>/status file.
func vmRSS(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			kb, err := strconv.ParseInt(f[0], 10, 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}
