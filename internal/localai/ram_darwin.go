//go:build darwin

package localai

import "golang.org/x/sys/unix"

// TotalRAM is the machine's physical memory in bytes (0 = unknown).
func TotalRAM() uint64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return n
}
