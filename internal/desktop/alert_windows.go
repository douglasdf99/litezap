package desktop

import (
	"syscall"
	"unsafe"
)

var messageBox = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")

// Alert shows a message box with an OK button and returns without waiting for it.
func Alert(title, msg string) error {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	m, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		return err
	}
	if err := messageBox.Find(); err != nil {
		return err
	}
	const flags = 0x00000040 | 0x00010000 | 0x00040000 // MB_ICONINFORMATION | MB_SETFOREGROUND | MB_TOPMOST
	go messageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return nil
}
