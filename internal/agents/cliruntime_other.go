//go:build !unix

package agents

import "os/exec"

// setProcessGroup: on other systems only the codex process itself is killed.
func setProcessGroup(*exec.Cmd) {}
