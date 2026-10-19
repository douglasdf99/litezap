package mcpb

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// RecordAppExe writes exe into the app folder dir (AppExeFile), only when it changed.
// The folder is kept private (0700): the extension trusts what it reads there.
func RecordAppExe(dir, exe string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	p := filepath.Join(dir, AppExeFile)
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == exe {
		return nil
	}
	tmp, err := os.CreateTemp(dir, "."+AppExeFile+"-*.tmp") // 0600, never an existing file
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after the rename
	_, werr := tmp.WriteString(exe + "\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(name, p)
}

// Executable names of the app: inside LiteZap.app (macOS) or the tray binary (Linux),
// and the Windows .exe. A file with any other name is never run, wherever the path came
// from (LITEZAP_APP_EXE and the app-exe file included).
const (
	exeUnix    = "litezap-app"
	exeWindows = "LiteZap.exe"
)

// Locator finds the app executable. Every field is replaceable in tests.
type Locator struct {
	GOOS   string
	Getenv func(string) string
	Home   func() (string, error)
	// Usable reports whether p is an existing regular file that only its owner can
	// change (see usable).
	Usable func(p string) bool
	// Spotlight lists the paths of app bundles with BundleID (macOS; nil: skipped).
	Spotlight func() []string
	ReadFile  func(string) ([]byte, error)
}

// NewLocator returns a Locator for this machine.
func NewLocator(goos string) *Locator {
	return &Locator{GOOS: goos, Getenv: os.Getenv, Home: os.UserHomeDir,
		Usable:    func(p string) bool { return usable(goos, p) },
		Spotlight: spotlight, ReadFile: os.ReadFile}
}

// usable: a regular file; on Unix also executable, and neither it nor its folder
// writable by group or others (someone else could swap it).
func usable(goos, p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if goos == "windows" {
		return true // ACLs, not mode bits: the permission bits say nothing there
	}
	perm := fi.Mode().Perm()
	if perm&0o111 == 0 || perm&0o022 != 0 {
		return false
	}
	di, err := os.Stat(filepath.Dir(p))
	return err == nil && di.Mode().Perm()&0o022 == 0
}

// spotlight asks Spotlight for LiteZap.app (macOS only).
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

// baseName is the last element of p for goos (Windows paths use \ or /).
func baseName(goos, p string) string {
	if goos == "windows" {
		return p[strings.LastIndexAny(p, `/\`)+1:]
	}
	return p[strings.LastIndex(p, "/")+1:]
}

// exeName reports whether p names the app executable.
func (l *Locator) exeName(p string) bool {
	if l.GOOS == "windows" {
		return strings.EqualFold(baseName(l.GOOS, p), exeWindows)
	}
	return baseName(l.GOOS, p) == exeUnix
}

// abs reports whether p is absolute for l.GOOS (C:\..., \\server\...; /... elsewhere).
func (l *Locator) abs(p string) bool {
	if l.GOOS == "windows" {
		return len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') || strings.HasPrefix(p, `\\`)
	}
	return strings.HasPrefix(p, "/")
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
		const inBundle = "Contents/MacOS/" + exeUnix
		c = append(c, filepath.Join("/Applications/LiteZap.app", inBundle))
		if home != "" {
			c = append(c, filepath.Join(home, "Applications", "LiteZap.app", inBundle))
		}
	case "windows":
		// Install folders only; a copy kept elsewhere (Documentos...) is found through the
		// app-exe file the app writes every time it opens.
		join := func(base string, parts ...string) {
			if base != "" {
				c = append(c, filepath.Join(append([]string{base}, parts...)...))
			}
		}
		join(l.Getenv("LOCALAPPDATA"), "Programs", "LiteZap", exeWindows)
		join(l.Getenv("LOCALAPPDATA"), "LiteZap", exeWindows)
		join(l.Getenv("ProgramFiles"), "LiteZap", exeWindows)
	default:
		if home != "" {
			c = append(c, filepath.Join(home, "LiteZap", exeUnix),
				filepath.Join(home, "Applications", "LiteZap", exeUnix),
				filepath.Join(home, ".local", "bin", exeUnix))
		}
		c = append(c, "/opt/LiteZap/"+exeUnix)
	}
	return c
}

// Find returns the first usable candidate. On macOS Spotlight is the last resort, limited
// to /Applications and ~/Applications and to bundles whose Info.plist says BundleID.
func (l *Locator) Find() (string, error) {
	for _, p := range l.Candidates() {
		if l.abs(p) && l.exeName(p) && l.Usable(p) {
			return p, nil
		}
	}
	if l.GOOS == "darwin" && l.Spotlight != nil {
		home, _ := l.Home()
		for _, app := range l.Spotlight() {
			app = filepath.Clean(app)
			parent := filepath.Dir(app)
			if parent != "/Applications" && (home == "" || parent != filepath.Join(home, "Applications")) {
				continue
			}
			if !l.bundleIDIs(app) {
				continue
			}
			if p := filepath.Join(app, "Contents", "MacOS", exeUnix); l.Usable(p) {
				return p, nil
			}
		}
	}
	return "", ErrAppNotFound
}

var bundleIDRe = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]*)</string>`)

// bundleIDIs reports whether the (XML) Info.plist of app says CFBundleIdentifier
// BundleID, as the one package-macos.sh writes.
func (l *Locator) bundleIDIs(app string) bool {
	b, err := l.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return false
	}
	m := bundleIDRe.FindSubmatch(b)
	return m != nil && string(m[1]) == BundleID
}
