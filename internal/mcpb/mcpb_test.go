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
		IsExec:   func(p string) bool { return exists[p] },
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RecordAppExe(dir, moved); err != nil {
		t.Fatal(err)
	}
	if p, _ := l.Find(); p != moved {
		t.Errorf("recorded: %q", p)
	}

	// LITEZAP_APP_EXE wins, but only when it exists.
	env[EnvAppExe] = "/gone/litezap-app"
	if p, _ := l.Find(); p != moved {
		t.Errorf("stale env: %q", p)
	}
	env[EnvAppExe] = "/opt/lz/litezap-app"
	exists["/opt/lz/litezap-app"] = true
	if p, _ := l.Find(); p != "/opt/lz/litezap-app" {
		t.Errorf("env: %q", p)
	}

	// Spotlight is the last resort.
	l2 := &Locator{GOOS: "darwin", Getenv: func(string) string { return "" }, Home: l.Home,
		IsExec:    func(p string) bool { return p == "/Users/x/Stuff/LiteZap.app/Contents/MacOS/litezap-app" },
		Spotlight: func() []string { return []string{"/Users/x/Stuff/LiteZap.app"} },
		ReadFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist }}
	if p, err := l2.Find(); err != nil || p != "/Users/x/Stuff/LiteZap.app/Contents/MacOS/litezap-app" {
		t.Errorf("spotlight: %q %v", p, err)
	}
}

func TestWindowsCandidates(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": `C:\Users\ana\AppData\Local`, "APPDATA": `C:\Users\ana\AppData\Roaming`}
	l := &Locator{GOOS: "windows", Getenv: func(k string) string { return env[k] },
		Home:     func() (string, error) { return `C:\Users\ana`, nil },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}
	c := l.Candidates()
	if !slices.Contains(c, filepath.Join(`C:\Users\ana\AppData\Local`, "Programs", "LiteZap", "LiteZap.exe")) ||
		!slices.Contains(c, filepath.Join(`C:\Users\ana`, "Documents", "LiteZap", "LiteZap.exe")) {
		t.Errorf("candidates %v", c)
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
