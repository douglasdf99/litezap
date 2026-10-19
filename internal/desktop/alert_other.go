//go:build !windows

package desktop

import (
	"errors"
	"os/exec"
	"runtime"
)

// Alert shows a small dialog with an OK button and returns without waiting for it
// (osascript on macOS; zenity, kdialog or notify-send on Linux).
func Alert(title, msg string) error {
	cmd := alertCommand(runtime.GOOS, title, msg, exec.LookPath)
	if cmd == nil {
		return errors.New("no dialog tool found (zenity, kdialog or notify-send)")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func alertCommand(goos, title, msg string, lookPath func(string) (string, error)) *exec.Cmd {
	if goos == "darwin" {
		// Text as arguments, never spliced into the script.
		return exec.Command("osascript",
			"-e", "on run argv",
			"-e", `display dialog (item 2 of argv) with title (item 1 of argv) buttons {"OK"} default button 1 with icon note`,
			"-e", "end run", title, msg)
	}
	if _, err := lookPath("zenity"); err == nil {
		return exec.Command("zenity", "--info", "--no-wrap", "--title", title, "--text", msg)
	}
	if _, err := lookPath("kdialog"); err == nil {
		return exec.Command("kdialog", "--title", title, "--msgbox", msg)
	}
	if _, err := lookPath("notify-send"); err == nil {
		return exec.Command("notify-send", "--", title, msg)
	}
	return nil
}
