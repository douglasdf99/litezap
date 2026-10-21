//go:build !darwin && !linux && !windows

package localai

// TotalRAM is unknown on this system (0).
func TotalRAM() uint64 { return 0 }
