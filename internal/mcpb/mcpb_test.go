package mcpb

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSemVer(t *testing.T) {
	for in, want := range map[string]string{
		"v0.1.0": "0.1.0", "0.1.0": "0.1.0", "v0.1.0-rc.1": "0.1.0-rc.1", "v0.0.0-test": "0.0.0-test",
		"dev": "0.0.0-dev", "": "0.0.0-dev", "v1.2": "0.0.0-1-2",
	} {
		if got := SemVer(in); got != want {
			t.Errorf("SemVer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManifest(t *testing.T) {
	m := NewManifest("v1.0.0", []string{"darwin", "win32"}, []Tool{{Name: "send_message"}})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	for _, k := range []string{"manifest_version", "name", "version", "description", "author", "server"} {
		if got[k] == nil {
			t.Errorf("required field %s missing", k)
		}
	}
	srv := got["server"].(map[string]any)
	cfg := srv["mcp_config"].(map[string]any)
	if srv["type"] != "binary" || srv["entry_point"] != ShimPath || cfg["command"] != "${__dirname}/"+ShimPath {
		t.Errorf("server: %v", srv)
	}
	if cfg["platform_overrides"].(map[string]any)["win32"].(map[string]any)["command"] != "${__dirname}/"+ShimPathExe {
		t.Errorf("win32 override: %v", cfg)
	}
	if NewManifest("v1", []string{"darwin"}, nil).Server.MCPConfig.PlatformOverrides != nil {
		t.Error("win32 override without a Windows binary")
	}
}

func TestPackAndCustomize(t *testing.T) {
	var buf bytes.Buffer
	files := []File{{Name: ShimPath, Data: []byte("bin"), Exec: true}, {Name: IconPath, Data: []byte("png")}}
	if err := Pack(&buf, NewManifest("v1.0.0", []string{"darwin"}, nil), files); err != nil {
		t.Fatal(err)
	}
	out, err := Customize(buf.Bytes(), map[string]string{EnvAppExe: "/x/litezap-app"})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == ShimPath && f.Mode().Perm() != 0o755 {
			t.Errorf("shim mode %v", f.Mode())
		}
		if f.Name == IconPath && f.Mode().Perm()&0o111 != 0 {
			t.Errorf("icon mode %v", f.Mode())
		}
	}
	if !reflect.DeepEqual(names, []string{"manifest.json", ShimPath, IconPath}) {
		t.Errorf("files %v", names)
	}
	m, err := ReadManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	env := m["server"].(map[string]any)["mcp_config"].(map[string]any)["env"].(map[string]any)
	if env[EnvAppExe] != "/x/litezap-app" || m["display_name"] != "LiteZap (WhatsApp)" {
		t.Errorf("manifest %v", m)
	}
	if _, err := Customize([]byte("not a zip"), nil); err == nil {
		t.Error("garbage accepted")
	}
	if err := Pack(&buf, Manifest{}, []File{{Name: "../evil"}}); err == nil {
		t.Error("path traversal accepted")
	}
}

func TestLocate(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	exists := map[string]bool{}
	l := &Locator{GOOS: "darwin", Getenv: func(k string) string { return env[k] },
		Home:     func() (string, error) { return home, nil },
		Usable:   func(p string) bool { return exists[p] },
		ReadFile: os.ReadFile}
	if _, err := l.Find(); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("nothing installed: %v", err)
	}

	std := "/Applications/LiteZap.app/Contents/MacOS/litezap-app"
	exists[std] = true
	if p, _ := l.Find(); p != std {
		t.Errorf("standard place: %q", p)
	}

	// The path the app recorded wins over the standard place.
	dir, _ := AppDirFor("darwin", l.Getenv, l.Home)
	moved := filepath.Join(home, "Apps", "LiteZap.app", "Contents", "MacOS", "litezap-app")
	exists[moved] = true
	if err := RecordAppExe(dir, moved); err != nil {
		t.Fatal(err)
	}
	if p, _ := l.Find(); p != moved {
		t.Errorf("recorded: %q", p)
	}

	// LITEZAP_APP_EXE wins, but only when it exists and names the app executable.
	env[EnvAppExe] = "/gone/litezap-app"
	if p, _ := l.Find(); p != moved {
		t.Errorf("stale env: %q", p)
	}
	env[EnvAppExe] = "/bin/sh"
	exists["/bin/sh"] = true
	if p, _ := l.Find(); p != moved {
		t.Errorf("env naming another program: %q", p)
	}
	env[EnvAppExe] = "relative/litezap-app"
	exists["relative/litezap-app"] = true
	if p, _ := l.Find(); p != moved {
		t.Errorf("relative env: %q", p)
	}
	env[EnvAppExe] = "/opt/lz/litezap-app"
	exists["/opt/lz/litezap-app"] = true
	if p, _ := l.Find(); p != "/opt/lz/litezap-app" {
		t.Errorf("env: %q", p)
	}
}

func TestRecordAppExe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LiteZap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{"/a/litezap-app", "/b/litezap-app", "/b/litezap-app"} {
		if err := RecordAppExe(dir, exe); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, AppExeFile)); string(b) != exe+"\n" {
			t.Errorf("app-exe = %q, want %q", b, exe)
		}
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("app dir mode %v, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, AppExeFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("app-exe mode %v, want 0600", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".*tmp")); len(m) != 0 {
		t.Errorf("temp files left: %v", m)
	}
}

func TestUsable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "litezap-app")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	set := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if !usable("darwin", p) {
		t.Fatal("0755 file in a 0755 folder refused")
	}
	set(p, 0o644)
	if usable("darwin", p) {
		t.Error("not executable accepted")
	}
	set(p, 0o775)
	if usable("darwin", p) {
		t.Error("group-writable file accepted")
	}
	set(p, 0o757)
	if usable("darwin", p) {
		t.Error("world-writable file accepted")
	}
	set(p, 0o755)
	set(dir, 0o777)
	if usable("darwin", p) {
		t.Error("file in a world-writable folder accepted")
	}
	set(dir, 0o755)
	if usable("darwin", dir) || usable("darwin", filepath.Join(dir, "nope")) {
		t.Error("folder or missing file accepted")
	}
}

func TestSpotlight(t *testing.T) {
	home := "/Users/ana"
	plist := func(id string) []byte {
		return []byte("<plist><dict>\n  <key>CFBundleIdentifier</key><string>" + id + "</string>\n</dict></plist>")
	}
	files := map[string][]byte{
		"/Users/ana/Applications/LiteZap.app/Contents/Info.plist": plist(BundleID),
		"/Applications/Fake.app/Contents/Info.plist":              plist("com.evil.app"),
		"/Users/ana/Downloads/LiteZap.app/Contents/Info.plist":    plist(BundleID),
	}
	// The standard places are missing: only what Spotlight returns can be found.
	standard := map[string]bool{
		"/Applications/LiteZap.app/Contents/MacOS/litezap-app":           true,
		"/Users/ana/Applications/LiteZap.app/Contents/MacOS/litezap-app": true,
	}
	l := &Locator{GOOS: "darwin", Getenv: func(string) string { return "" },
		Home:   func() (string, error) { return home, nil },
		Usable: func(p string) bool { return !standard[p] && strings.HasSuffix(p, "/Contents/MacOS/litezap-app") },
		ReadFile: func(p string) ([]byte, error) {
			if b, ok := files[p]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		}}
	for _, c := range []struct {
		hits []string
		want string
	}{
		{[]string{"/Users/ana/Downloads/LiteZap.app"}, ""}, // outside the Applications folders
		{[]string{"/Applications/Fake.app"}, ""},           // another bundle id
		{[]string{"/Applications/Missing.app"}, ""},        // no Info.plist
		{[]string{"/Users/ana/Downloads/LiteZap.app", "/Applications/Fake.app"}, ""},
	} {
		l.Spotlight = func() []string { return c.hits }
		if p, _ := l.Find(); p != c.want {
			t.Errorf("hits %v: %q, want %q", c.hits, p, c.want)
		}
	}

	// A good hit: ~/Applications with the right bundle id.
	files["/Users/ana/Applications/LiteZap 2.app/Contents/Info.plist"] = plist(BundleID)
	l.Spotlight = func() []string { return []string{"/Users/ana/Applications/LiteZap 2.app"} }
	if p, err := l.Find(); err != nil || p != "/Users/ana/Applications/LiteZap 2.app/Contents/MacOS/litezap-app" {
		t.Errorf("good hit: %q %v", p, err)
	}
}

func TestWindowsCandidates(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": `C:\Users\ana\AppData\Local`, "APPDATA": `C:\Users\ana\AppData\Roaming`,
		EnvAppExe: `C:\Users\ana\Documents\LiteZap\LiteZap.exe`}
	l := &Locator{GOOS: "windows", Getenv: func(k string) string { return env[k] },
		Home:     func() (string, error) { return `C:\Users\ana`, nil },
		Usable:   func(string) bool { return true },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}
	c := l.Candidates()
	if !slices.Contains(c, filepath.Join(`C:\Users\ana\AppData\Local`, "Programs", "LiteZap", "LiteZap.exe")) {
		t.Errorf("candidates %v", c)
	}
	for _, p := range c[1:] {
		if strings.Contains(p, "Documents") || strings.Contains(p, "Downloads") || strings.Contains(p, "Desktop") {
			t.Errorf("user folder among the default candidates: %v", c)
		}
	}
	if p, _ := l.Find(); p != env[EnvAppExe] {
		t.Errorf("env: %q", p)
	}
	env[EnvAppExe] = `C:\Users\ana\Downloads\evil.exe`
	if p, _ := l.Find(); p == env[EnvAppExe] {
		t.Error("an exe with another name was accepted")
	}
	env[EnvAppExe] = `C:\Users\ana\Documents\LiteZap\litezap.EXE`
	if p, _ := l.Find(); p != env[EnvAppExe] {
		t.Errorf("case-insensitive name: %q", p)
	}
}

func TestAppDirFor(t *testing.T) {
	home := func() (string, error) { return "/h", nil }
	if d, _ := AppDirFor("darwin", func(string) string { return "" }, home); d != filepath.Join("/h", "Library", "Application Support", "LiteZap") {
		t.Errorf("darwin %q", d)
	}
	if _, err := AppDirFor("linux", func(k string) string { return map[string]string{"LITEZAP_APP_DIR": "rel"}[k] }, home); err == nil {
		t.Error("relative LITEZAP_APP_DIR accepted")
	}
}
