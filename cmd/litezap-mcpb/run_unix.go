//go:build !windows

package main

import (
	"os"
	"syscall"
)

// run replaces this process with the app: stdin, stdout and signals go straight to it.
func run(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
