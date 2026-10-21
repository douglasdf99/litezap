package localai

import (
	"strconv"
	"strings"
)

// parseMeminfo reads MemTotal (in kB) from the text of /proc/meminfo.
func parseMeminfo(s string) uint64 {
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "MemTotal:"))
		if len(f) == 0 {
			return 0
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		return n
	}
	return 0
}
