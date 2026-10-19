// Package mcpb builds the LiteZap Claude Desktop extension (an MCP Bundle, .mcpb: a zip
// with manifest.json, an icon and a server binary) and holds the logic of that binary, a
// small shim (cmd/litezap-mcpb) that finds the installed LiteZap app and runs
// `<app> mcp`. The MCP server itself stays in the app: the extension never carries a
// second copy of the tools that could drift from the installed version.
//
// It only uses the standard library so the shim stays small.
//
// The shim only runs a file named litezap-app (LiteZap.exe on Windows) that neither it
// nor its folder are writable by group or others (Unix), from LITEZAP_APP_EXE, the
// app-exe file of the app folder or install folders; Spotlight hits only count under
// /Applications or ~/Applications with CFBundleIdentifier dev.litezap.app.
package mcpb

import (
	"errors"
	"path/filepath"
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
