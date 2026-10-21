//go:build unix

package agents

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts codex (a node script) and its children in their own process group,
// killed as a whole when the run ends, times out or breaks a rule.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// checkFilePerms is the Unix trust check: owned by root or by the user running LiteZap, and
// not writable by group or others (a folder may be, with the sticky bit, like /tmp).
func checkFilePerms(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if st.Uid != 0 && int(st.Uid) != os.Getuid() {
		return false
	}
	if fi.Mode().Perm()&0o022 == 0 {
		return true
	}
	return fi.IsDir() && fi.Mode()&os.ModeSticky != 0
}
