//go:build !unix

package agents

import (
	"os"
	"os/exec"
)

// setProcessGroup: on other systems only the codex process itself is killed.
func setProcessGroup(*exec.Cmd) {}

// checkFilePerms: Windows ACLs are not checked here (the install folders are per user).
func checkFilePerms(os.FileInfo) bool { return true }
