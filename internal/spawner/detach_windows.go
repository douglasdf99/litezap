//go:build windows

package spawner

import "os/exec"

func detach(cmd *exec.Cmd) {}
