package desktop

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/mcpb"
)

func TestBundledExtension(t *testing.T) {
	if got, want := BundledExtension("darwin", "/Applications/LiteZap.app/Contents/MacOS/litezap-app"),
		filepath.Join("/Applications/LiteZap.app/Contents/Resources", mcpb.FileName); got != want {
		t.Errorf("darwin: %q, want %q", got, want)
	}
	if got, want := BundledExtension("windows", filepath.Join("C:", "LiteZap", "LiteZap.exe")),
		filepath.Join("C:", "LiteZap", mcpb.FileName); got != want {
		t.Errorf("windows: %q, want %q", got, want)
	}
}

func TestInstallExtension(t *testing.T) {
	c := testConnector(t)
	c.Env = map[string]string{"LITEZAP_APP_DIR": "/data/lz"}
	c.CacheDir = t.TempDir()
	var opened []string
	c.Open = func(p string) error { opened = append(opened, p); return nil }

	if _, err := c.InstallExtension(); !errors.Is(err, ErrNoExtension) {
		t.Fatalf("no bundle: %v", err)
	}
	if c.Status(ClaudeDesktop).Extension {
		t.Error("extension offered without a bundle")
	}

	var buf bytes.Buffer
	m := mcpb.NewManifest("v1.2.3", []string{"darwin"}, nil)
	if err := mcpb.Pack(&buf, m, []mcpb.File{{Name: mcpb.ShimPath, Data: []byte("shim"), Exec: true}}); err != nil {
		t.Fatal(err)
	}
	c.Extension = filepath.Join(t.TempDir(), mcpb.FileName)
	if err := os.WriteFile(c.Extension, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.Status(ClaudeDesktop).Extension || c.Status(Cursor).Extension {
		t.Error("extension availability")
	}
	res, err := c.InstallExtension()
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != res.Path || filepath.Dir(res.Path) != c.CacheDir {
		t.Fatalf("opened %v, result %+v", opened, res)
	}
	b, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	man, err := mcpb.ReadManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	env := man["server"].(map[string]any)["mcp_config"].(map[string]any)["env"].(map[string]any)
	if env[mcpb.EnvAppExe] != testExe || env["LITEZAP_APP_DIR"] != "/data/lz" || man["version"] != "1.2.3" {
		t.Errorf("manifest: %v", man)
	}

	c.Exe = "/Volumes/LiteZap/LiteZap.app/Contents/MacOS/litezap-app"
	if _, err := c.InstallExtension(); !errors.Is(err, ErrExeNotInstalled) {
		t.Errorf("app inside the dmg: %v", err)
	}
}

func TestInstallExtensionRemovesManualEntry(t *testing.T) {
	f := &fakeClaude{}
	c := guardConnector(t, f)
	c.CacheDir = t.TempDir()
	c.Open = func(string) error { return nil }
	var buf bytes.Buffer
	if err := mcpb.Pack(&buf, mcpb.NewManifest("v1.2.3", []string{"darwin"}, nil),
		[]mcpb.File{{Name: mcpb.ShimPath, Data: []byte("shim"), Exec: true}}); err != nil {
		t.Fatal(err)
	}
	c.Extension = filepath.Join(t.TempDir(), mcpb.FileName)
	if err := os.WriteFile(c.Extension, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// No manual entry: nothing removed, no guard.
	res, err := c.InstallExtension()
	if err != nil || res.ManualRemoved || res.ManualError != "" || res.Hint != ExtensionReadyHint {
		t.Fatalf("no entry: %+v, %v", res, err)
	}
	// The extension path never tells the user to restart Claude.
	for _, s := range []string{res.Hint, manualRemovedHint} {
		if l := strings.ToLower(s); strings.Contains(l, "reinicie") || strings.Contains(l, "cmd+q") || strings.Contains(l, "feche") {
			t.Errorf("restart wording in the extension path: %q", s)
		}
	}

	// Manual entry and Claude open: removed now, and again after Claude writes it back.
	restoreEntry(t, c)
	f.open.Store(true)
	res, err = c.InstallExtension()
	if err != nil || !res.ManualRemoved || !res.Running || res.ManualHint != manualRemovedHint ||
		res.Hint != ExtensionReadyHint {
		t.Fatalf("install: %+v, %v", res, err)
	}
	if c.Status(ClaudeDesktop).Present {
		t.Fatal("manual entry not removed")
	}
	restoreEntry(t, c)
	f.open.Store(false)
	eventually(t, "re-remove", func() bool { return !c.Status(ClaudeDesktop).Present })
	f.open.Store(true)
	waitGuard(t, c)
	if c.Status(ClaudeDesktop).Present || f.notifications() != 1 {
		t.Errorf("present %v, %d notifications", c.Status(ClaudeDesktop).Present, f.notifications())
	}

	// A malformed config is not touched (Status reports the error): the install still succeeds.
	p, _ := c.ConfigPath(ClaudeDesktop)
	writeFile(t, p, `{"mcpServers":{"litezap":{}}`)
	f.open.Store(false)
	if res, err = c.InstallExtension(); err != nil || res.ManualRemoved {
		t.Errorf("malformed: %+v, %v", res, err)
	}
}

func TestExtensionInstalled(t *testing.T) {
	c := testConnector(t)
	if st := c.Status(ClaudeDesktop); st.ExtensionInstalled || st.ExtensionDisabled {
		t.Fatal("installed with no extensions folder")
	}
	p, _ := c.ConfigPath(ClaudeDesktop)
	base := filepath.Dir(p)
	writeFile(t, filepath.Join(base, "Claude Extensions", "other", "manifest.json"), `{"name":"other"}`)
	if c.Status(ClaudeDesktop).ExtensionInstalled {
		t.Fatal("another extension seen as LiteZap")
	}

	// The folder Claude Desktop really uses (seen on macOS), switch on.
	writeFile(t, filepath.Join(base, "Claude Extensions", ExtensionID, "manifest.json"), `{"name":"litezap"}`)
	writeFile(t, filepath.Join(base, "Claude Extensions Settings", ExtensionID+".json"), `{"isEnabled": true}`)
	st := c.Status(ClaudeDesktop)
	if !st.ExtensionInstalled || st.ExtensionDisabled || !strings.Contains(st.Hint, "Conectado via extensão") {
		t.Errorf("installed extension: %+v", st)
	}
	writeFile(t, filepath.Join(base, "Claude Extensions Settings", ExtensionID+".json"), `{"isEnabled": false}`)
	if st := c.Status(ClaudeDesktop); !st.ExtensionInstalled || !st.ExtensionDisabled {
		t.Errorf("disabled extension: %+v", st)
	}

	// Another folder name (a signed copy, a future id): found by the manifest name.
	if err := os.RemoveAll(filepath.Join(base, "Claude Extensions", ExtensionID)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "Claude Extensions", "ant.dir.litezap", "manifest.json"), `{"name":"litezap"}`)
	if st := c.Status(ClaudeDesktop); !st.ExtensionInstalled || st.ExtensionDisabled {
		t.Errorf("extension under another id: %+v", st)
	}
}

func TestClaudeCodeNoRestart(t *testing.T) {
	c := testConnector(t)
	st := c.Status(ClaudeCode)
	if st.Hint != ClaudeCodeHint || !strings.Contains(st.Hint, "/mcp") || strings.Contains(strings.ToLower(st.Hint), "reinicie") {
		t.Errorf("hint: %q", st.Hint)
	}
	if len(st.PluginCommands) != 2 || st.PluginCommands[1] != "/plugin install litezap@litezap" {
		t.Errorf("plugin commands: %q", st.PluginCommands)
	}
	if c.Status(Cursor).PluginCommands != nil {
		t.Error("plugin commands outside Claude Code")
	}
}
