//go:build windows

package localai

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// TotalRAM is the machine's physical memory in bytes (0 = unknown; best effort).
func TotalRAM() uint64 {
	if procGlobalMemoryStatusEx.Find() != nil {
		return 0
	}
	m := memoryStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return m.TotalPhys
}
