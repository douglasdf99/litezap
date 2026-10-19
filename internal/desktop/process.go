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
//	Windows: Claude.exe whose path is the Claude Desktop install (…\AnthropicClaude\… or
//	         …\Claude\app-…: the Claude Code CLI is also claude.exe and does not count),
//	         or Cursor.exe
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
		switch id {
		case ClaudeDesktop:
			// Name filters are case-insensitive in WQL: claude.exe (Claude Code) matches
			// too, and is told apart by its path. No answer (no PowerShell, an error):
			// unknown, reported as not running.
			cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
				`Get-CimInstance Win32_Process -Filter "Name='Claude.exe'" | ForEach-Object { $_.ExecutablePath }`)
			hideWindow(cmd)
			out, err := cmd.Output()
			return err == nil && claudeDesktopPath(string(out))
		case Cursor:
			cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq Cursor.exe", "/NH", "/FO", "CSV")
			hideWindow(cmd)
			out, err := cmd.Output()
			return err == nil && strings.Contains(strings.ToLower(string(out)), `"cursor.exe"`)
		}
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

// claudeDesktopPath reports whether one of the executable paths (one per line) is the
// Claude Desktop app: %LOCALAPPDATA%\AnthropicClaude\app-<version>\claude.exe (Squirrel
// install) or a ...\Claude\app-<version>\ folder.
func claudeDesktopPath(paths string) bool {
	for _, line := range strings.Split(paths, "\n") {
		p := strings.ToLower(strings.TrimSpace(line))
		if p != "" && (strings.Contains(p, `\anthropicclaude\`) || strings.Contains(p, `\claude\app-`)) {
			return true
		}
	}
	return false
}
