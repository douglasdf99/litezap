// Package mcpb builds the LiteZap Claude Desktop extension (an MCP Bundle, .mcpb: a zip
// with manifest.json, an icon and a server binary) and holds the logic of that binary, a
// small shim (cmd/litezap-mcpb) that finds the installed LiteZap app and runs
// `<app> mcp`. The MCP server itself stays in the app: the extension never carries a
// second copy of the tools that could drift from the installed version.
//
// It only uses the standard library so the shim stays small.
package mcpb

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BundleID is the CFBundleIdentifier of LiteZap.app (cmd/litezap-app/package-macos.sh).
const BundleID = "dev.litezap.app"

// AppExeFile is the file in the app folder where the running app records the path of its
// executable, so the extension finds the app wherever the user put it.
const AppExeFile = "app-exe"

// EnvAppExe points the shim at the app executable (set by the extension the app itself
// generates; a stale value is ignored).
const EnvAppExe = "LITEZAP_APP_EXE"

// ErrAppNotFound is returned when no LiteZap app was found on this computer.
var ErrAppNotFound = errors.New("o app LiteZap não foi encontrado neste computador: instale-o (https://litezap.com/docs/baixar/), " +
	"abra-o uma vez e tente de novo")

// AppDirFor is the per-user folder of the desktop app (see desktop.AppDir).
//
//	macOS:   ~/Library/Application Support/LiteZap
//	Windows: %APPDATA%\LiteZap
//	Linux:   $XDG_DATA_HOME/litezap or ~/.local/share/litezap
//
// LITEZAP_APP_DIR (absolute) overrides it.
func AppDirFor(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	if d := getenv("LITEZAP_APP_DIR"); d != "" {
		if !filepath.IsAbs(d) {
			return "", errors.New("LITEZAP_APP_DIR must be an absolute path")
		}
		return d, nil
	}
	switch goos {
	case "darwin":
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", "LiteZap"), nil
	case "windows":
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "LiteZap"), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Roaming", "LiteZap"), nil
	default:
		// The XDG spec says relative values are invalid and must be ignored.
		if d := getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, "litezap"), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		if h == "" {
			return "", errors.New("no home directory")
		}
		return filepath.Join(h, ".local", "share", "litezap"), nil
	}
}

// RecordAppExe writes exe into the app folder dir (AppExeFile), only when it changed.
func RecordAppExe(dir, exe string) error {
	p := filepath.Join(dir, AppExeFile)
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == exe {
		return nil
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(exe+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Locator finds the app executable. Every field is replaceable in tests.
type Locator struct {
	GOOS   string
	Getenv func(string) string
	Home   func() (string, error)
	// IsExec reports whether p is an existing regular file (executable on Unix).
	IsExec func(p string) bool
	// Spotlight lists the paths of app bundles with BundleID (macOS; nil: skipped).
	Spotlight func() []string
	ReadFile  func(string) ([]byte, error)
}

// NewLocator returns a Locator for this machine.
func NewLocator(goos string) *Locator {
	return &Locator{GOOS: goos, Getenv: os.Getenv, Home: os.UserHomeDir, IsExec: isExec,
		Spotlight: spotlight, ReadFile: os.ReadFile}
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return filepath.Ext(p) == ".exe" || fi.Mode().Perm()&0o111 != 0
}

// spotlight asks Spotlight for LiteZap.app wherever it is (macOS only).
func spotlight() []string {
	out, err := exec.Command("mdfind", "kMDItemCFBundleIdentifier == '"+BundleID+"'").Output()
	if err != nil {
		return nil
	}
	var apps []string
	for _, line := range strings.Split(string(out), "\n") { // paths may contain spaces
		if line = strings.TrimSpace(line); line != "" {
			apps = append(apps, line)
		}
	}
	return apps
}

// Candidates are the places checked for the app executable, in order: LITEZAP_APP_EXE,
// the path the app recorded in its folder, then the usual install places.
func (l *Locator) Candidates() []string {
	var c []string
	if p := strings.TrimSpace(l.Getenv(EnvAppExe)); p != "" {
		c = append(c, p)
	}
	if dir, err := AppDirFor(l.GOOS, l.Getenv, l.Home); err == nil {
		if b, err := l.ReadFile(filepath.Join(dir, AppExeFile)); err == nil {
			if p := strings.TrimSpace(string(b)); p != "" {
				c = append(c, p)
			}
		}
	}
	home, _ := l.Home()
	switch l.GOOS {
	case "darwin":
		const inBundle = "Contents/MacOS/litezap-app"
		c = append(c, filepath.Join("/Applications/LiteZap.app", inBundle))
		if home != "" {
			c = append(c, filepath.Join(home, "Applications", "LiteZap.app", inBundle))
		}
	case "windows":
		join := func(base string, parts ...string) {
			if base != "" {
				c = append(c, filepath.Join(append([]string{base}, parts...)...))
			}
		}
		join(l.Getenv("LOCALAPPDATA"), "Programs", "LiteZap", "LiteZap.exe")
		join(l.Getenv("LOCALAPPDATA"), "LiteZap", "LiteZap.exe")
		join(l.Getenv("ProgramFiles"), "LiteZap", "LiteZap.exe")
		for _, sub := range []string{"Documents", "Desktop", "Downloads"} {
			join(home, sub, "LiteZap", "LiteZap.exe")
			join(home, sub, "LiteZap-windows", "LiteZap.exe")
			join(home, sub, "LiteZap.exe")
		}
	default:
		if home != "" {
			c = append(c, filepath.Join(home, "LiteZap", "litezap-app"),
				filepath.Join(home, "Applications", "LiteZap", "litezap-app"),
				filepath.Join(home, ".local", "bin", "litezap-app"))
		}
		c = append(c, "/opt/LiteZap/litezap-app")
	}
	return c
}

// Find returns the first candidate that exists; on macOS Spotlight is the last resort.
func (l *Locator) Find() (string, error) {
	for _, p := range l.Candidates() {
		if filepath.IsAbs(p) && l.IsExec(p) {
			return p, nil
		}
	}
	if l.GOOS == "darwin" && l.Spotlight != nil {
		for _, app := range l.Spotlight() {
			p := filepath.Join(app, "Contents", "MacOS", "litezap-app")
			if filepath.IsAbs(p) && l.IsExec(p) {
				return p, nil
			}
		}
	}
	return "", ErrAppNotFound
}
