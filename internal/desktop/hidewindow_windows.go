//go:build windows

package desktop

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console tool (powershell, tasklist) run by the GUI app from flashing
// a console window.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
