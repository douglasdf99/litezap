// Package desktop holds the OS integration of the tray launcher (cmd/litezap-app): the
// per-user app folder, opening URLs and folders, and the tray icon. It has no CGO and no
// GUI dependency, so it is tested on every platform.
package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// AppDir is the per-user folder of the tray app: config (litezap.yaml), data/ and logs.
//
//	macOS:   ~/Library/Application Support/LiteZap
//	Windows: %APPDATA%\LiteZap
//	Linux:   $XDG_DATA_HOME/litezap or ~/.local/share/litezap
//
// LITEZAP_APP_DIR (absolute) overrides it, e.g. to keep a test run apart.
func AppDir() (string, error) { return appDir(runtime.GOOS, os.Getenv, os.UserHomeDir) }

func appDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
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

// Open opens a URL in the default browser, or a file/folder in the file manager.
func Open(target string) error {
	cmd := openCommand(runtime.GOOS, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func openCommand(goos, target string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		// rundll32 hands the target to the shell without cmd.exe parsing it ("&" in a URL
		// would split a `cmd /c start` command line).
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}
