//go:build unix

package agents

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts codex (a node script) and its children in their own process group,
// killed as a whole when the run ends or times out.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
