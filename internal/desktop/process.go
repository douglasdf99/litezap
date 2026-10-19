package desktop

import (
	"os/exec"
	"strings"
)

// ClientRunning reports whether the AI client id is open on this computer (best effort:
// false when unknown). Claude Code has no single process to look for: always false.
//
//	macOS:   a process whose executable is <...>/Claude.app/Contents/MacOS/Claude
//	         (bundle id com.anthropic.claudefordesktop) or <...>/Cursor.app/Contents/MacOS/Cursor
//	Windows: an image named Claude.exe or Cursor.exe (tasklist)
func ClientRunning(goos, id string) bool {
	switch goos {
	case "darwin":
		suffix := map[string]string{ClaudeDesktop: "/Claude.app/Contents/MacOS/Claude", Cursor: "/Cursor.app/Contents/MacOS/Cursor"}[id]
		if suffix == "" {
			return false
		}
		out, err := exec.Command("ps", "-axo", "comm=").Output()
		return err == nil && hasProcessSuffix(string(out), suffix)
	case "windows":
		image := map[string]string{ClaudeDesktop: "Claude.exe", Cursor: "Cursor.exe"}[id]
		if image == "" {
			return false
		}
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/NH", "/FO", "CSV").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), `"`+strings.ToLower(image)+`"`)
	}
	return false
}

// hasProcessSuffix reports whether a line of `ps -axo comm=` ends with suffix.
func hasProcessSuffix(ps, suffix string) bool {
	for _, line := range strings.Split(ps, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), suffix) {
			return true
		}
	}
	return false
}
