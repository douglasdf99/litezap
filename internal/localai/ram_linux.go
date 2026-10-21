//go:build linux

package localai

import "os"

// TotalRAM is the machine's physical memory in bytes (0 = unknown), from /proc/meminfo.
func TotalRAM() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMeminfo(string(b))
}
