package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testExe = "/Applications/LiteZap.app/Contents/MacOS/litezap-app"

// testConnector works only inside a temp home and never finds a claude CLI.
func testConnector(t *testing.T) *Connector {
	t.Helper()
	return &Connector{Exe: testExe, GOOS: "darwin", Home: t.TempDir(),
		Getenv: func(string) string { return "" }}
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", p, err, b)
	}
	return m
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPaths(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		goos, id string
		env      map[string]string
		want     string
	}{
		{"darwin", ClaudeDesktop, nil, filepath.Join("/h", "Library", "Application Support", "Claude", "claude_desktop_config.json")},
		{"windows", ClaudeDesktop, map[string]string{"APPDATA": `C:\Users\ana\AppData\Roaming`}, filepath.Join(`C:\Users\ana\AppData\Roaming`, "Claude", "claude_desktop_config.json")},
		{"linux", ClaudeDesktop, nil, filepath.Join("/h", ".config", "Claude", "claude_desktop_config.json")},
		{"linux", ClaudeDesktop, map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "Claude", "claude_desktop_config.json")},
		{"darwin", ClaudeCode, nil, filepath.Join("/h", ".claude.json")},
		{"darwin", ClaudeCode, map[string]string{"CLAUDE_CONFIG_DIR": "/cc"}, filepath.Join("/cc", ".claude.json")},
		{"windows", Cursor, nil, filepath.Join("/h", ".cursor", "mcp.json")},
	}
	for _, c := range cases {
		conn := &Connector{GOOS: c.goos, Home: "/h", Getenv: env(c.env)}
		got, err := conn.ConfigPath(c.id)
		if err != nil || got != c.want {
			t.Errorf("%s %s %v: got %q, %v; want %q", c.goos, c.id, c.env, got, err, c.want)
		}
	}
	if _, err := (&Connector{Getenv: env(nil)}).ConfigPath("chatgpt"); err == nil {
		t.Error("unknown client accepted")
	}
}

func TestConnectNewFile(t *testing.T) {
	c := testConnector(t)
	ctx := context.Background()
	res, err := c.Connect(ctx, ClaudeDesktop)
	if err != nil || !res.Changed || res.Backup != "" {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	m := readJSON(t, res.Path)
	want := map[string]any{"command": testExe, "args": []any{"mcp"}}
	if got := m["mcpServers"].(map[string]any)[ServerName]; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry = %v, want %v", got, want)
	}
	if fi, _ := os.Stat(res.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v, want 0600", fi.Mode().Perm())
	}
	if st := c.Status(ClaudeDesktop); !st.Connected || !st.Present || !st.Installed || st.Error != "" {
		t.Errorf("status after connect: %+v", st)
	}
}

func TestConnectMergesAndBacksUp(t *testing.T) {
	c := testConnector(t)
	ctx := context.Background()
	p, _ := c.ConfigPath(ClaudeDesktop)
	orig := `{
  "globalShortcut": "Ctrl+Space",
  "mcpServers": {
    "filesystem": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/ana/<docs>&"]}
  }
}`
	writeFile(t, p, orig)
	if st := c.Status(ClaudeDesktop); st.Connected || st.Present {
		t.Fatalf("status before: %+v", st)
	}
	res, err := c.Connect(ctx, ClaudeDesktop)
	if err != nil || !res.Changed {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	if res.Backup != p+BackupSuffix {
		t.Fatalf("backup = %q", res.Backup)
	}
	if b, _ := os.ReadFile(res.Backup); string(b) != orig {
		t.Fatalf("backup content changed:\n%s", b)
	}
	m := readJSON(t, p)
	if m["globalShortcut"] != "Ctrl+Space" {
		t.Error("other top-level key lost")
	}
	servers := m["mcpServers"].(map[string]any)
	fsArgs := servers["filesystem"].(map[string]any)["args"].([]any)
	if len(servers) != 2 || fsArgs[2] != "/Users/ana/<docs>&" {
		t.Errorf("other server changed: %v", servers)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), `\u003c`) {
		t.Error("HTML escaping rewrote other values")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode not kept: %v", fi.Mode().Perm())
	}

	// Idempotent: a second connect changes nothing and makes no new backup.
	before, _ := os.ReadFile(p)
	res, err = c.Connect(ctx, ClaudeDesktop)
	if err != nil || res.Changed || res.Backup != "" {
		t.Fatalf("second connect: %+v, %v", res, err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("second connect rewrote the file")
	}

	// Disconnect removes only our entry; the first backup stays the original.
	res, err = c.Disconnect(ctx, ClaudeDesktop)
	if err != nil || !res.Changed || res.Backup != "" {
		t.Fatalf("disconnect: %+v, %v", res, err)
	}
	m = readJSON(t, p)
	servers = m["mcpServers"].(map[string]any)
	if _, ok := servers[ServerName]; ok || len(servers) != 1 || servers["filesystem"] == nil {
		t.Errorf("after disconnect: %v", servers)
	}
	if b, _ := os.ReadFile(p + BackupSuffix); string(b) != orig {
		t.Error("backup overwritten")
	}
	if st := c.Status(ClaudeDesktop); st.Connected || st.Present {
		t.Errorf("status after disconnect: %+v", st)
	}
	// Disconnecting again is a no-op.
	if res, err := c.Disconnect(ctx, ClaudeDesktop); err != nil || res.Changed {
		t.Errorf("second disconnect: %+v, %v", res, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

func TestConnectFixesStaleEntry(t *testing.T) {
	c := testConnector(t)
	p, _ := c.ConfigPath(Cursor)
	writeFile(t, p, `{"mcpServers":{"litezap":{"command":"/old/LiteZap.app/Contents/MacOS/litezap-app","args":["mcp"]},"other":{"url":"http://x"}}}`)
	st := c.Status(Cursor)
	if !st.Present || st.Connected {
		t.Fatalf("stale status: %+v", st)
	}
	if _, err := c.Connect(context.Background(), Cursor); err != nil {
		t.Fatal(err)
	}
	servers := readJSON(t, p)["mcpServers"].(map[string]any)
	if servers[ServerName].(map[string]any)["command"] != testExe || servers["other"] == nil {
		t.Errorf("servers = %v", servers)
	}
}

func TestMalformedConfigIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"syntax":         `{"mcpServers": {`,
		"array":          `[1, 2]`,
		"null":           `null`,
		"servers string": `{"mcpServers": "nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := testConnector(t)
			p, _ := c.ConfigPath(ClaudeDesktop)
			writeFile(t, p, content)
			if st := c.Status(ClaudeDesktop); st.Error == "" || st.Connected {
				t.Errorf("status: %+v", st)
			}
			for _, op := range []func(context.Context, string) (AIResult, error){c.Connect, c.Disconnect} {
				if _, err := op(context.Background(), ClaudeDesktop); !errors.Is(err, ErrMalformedConfig) {
					t.Errorf("err = %v, want ErrMalformedConfig", err)
				}
			}
			if b, _ := os.ReadFile(p); string(b) != content {
				t.Error("malformed file was modified")
			}
			if _, err := os.Stat(p + BackupSuffix); err == nil {
				t.Error("backup written for a refused file")
			}
		})
	}
}

func TestEmptyFileIsEmptyConfig(t *testing.T) {
	c := testConnector(t)
	p, _ := c.ConfigPath(Cursor)
	writeFile(t, p, "  \n")
	if _, err := c.Connect(context.Background(), Cursor); err != nil {
		t.Fatal(err)
	}
	if readJSON(t, p)["mcpServers"].(map[string]any)[ServerName] == nil {
		t.Error("entry missing")
	}
}

func TestConnectKeepsSymlink(t *testing.T) {
	c := testConnector(t)
	p, _ := c.ConfigPath(Cursor)
	real := filepath.Join(c.Home, "dotfiles", "mcp.json")
	writeFile(t, real, `{"mcpServers":{}}`)
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.Symlink(real, p); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := c.Connect(context.Background(), Cursor); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(p); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced by a file")
	}
	if readJSON(t, real)["mcpServers"].(map[string]any)[ServerName] == nil {
		t.Error("target not updated")
	}
}

func TestClaudeCodeFileFallback(t *testing.T) {
	c := testConnector(t)
	c.Env = map[string]string{"LITEZAP_APP_DIR": "/tmp/lz"}
	p, _ := c.ConfigPath(ClaudeCode)
	writeFile(t, p, `{"numStartups": 3, "projects": {"/x": {"mcpServers": {"p": {}}}}}`)
	if _, err := c.Connect(context.Background(), ClaudeCode); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, p)
	e := m["mcpServers"].(map[string]any)[ServerName].(map[string]any)
	if e["type"] != "stdio" || e["command"] != testExe || e["env"].(map[string]any)["LITEZAP_APP_DIR"] != "/tmp/lz" {
		t.Errorf("entry = %v", e)
	}
	if m["numStartups"] != float64(3) || m["projects"] == nil {
		t.Error("other keys lost")
	}
	if !c.Status(ClaudeCode).Connected {
		t.Error("not connected")
	}
	// A different LITEZAP_APP_DIR is a different connection.
	c.Env["LITEZAP_APP_DIR"] = "/tmp/other"
	if c.Status(ClaudeCode).Connected {
		t.Error("env mismatch reported as connected")
	}
}

func TestClaudeCodeUsesCLI(t *testing.T) {
	c := testConnector(t)
	var calls [][]string
	c.LookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	c.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	p, _ := c.ConfigPath(ClaudeCode)
	writeFile(t, p, `{"mcpServers":{"litezap":{"command":"/old","args":["mcp"]}}}`)
	res, err := c.Connect(context.Background(), ClaudeCode)
	if err != nil || !res.CLI || !res.Changed {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	want := [][]string{
		{"/usr/local/bin/claude", "mcp", "remove", "--scope", "user", "litezap"},
		{"/usr/local/bin/claude", "mcp", "add", "--scope", "user", "litezap", "--", testExe, "mcp"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %q\nwant  %q", calls, want)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "/old") {
		t.Error("file edited although the CLI succeeded")
	}

	// A failing CLI falls back to editing the file.
	c.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("boom"), errors.New("exit 1") }
	if res, err := c.Connect(context.Background(), ClaudeCode); err != nil || res.CLI {
		t.Fatalf("fallback: %+v, %v", res, err)
	}
	if !c.Status(ClaudeCode).Connected {
		t.Error("fallback did not connect")
	}

	calls = nil
	c.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	if res, err := c.Disconnect(context.Background(), ClaudeCode); err != nil || !res.CLI {
		t.Fatalf("disconnect: %+v, %v", res, err)
	}
	if len(calls) != 1 || calls[0][2] != "remove" {
		t.Errorf("disconnect calls = %q", calls)
	}
}

func TestExeProblem(t *testing.T) {
	for exe, bad := range map[string]bool{
		testExe: false,
		"/private/var/folders/x/AppTranslocation/ABC/d/LiteZap.app/Contents/MacOS/litezap-app": true,
		"/Volumes/LiteZap/LiteZap.app/Contents/MacOS/litezap-app":                              true,
	} {
		c := &Connector{Exe: exe, GOOS: "darwin", Home: t.TempDir(), Getenv: func(string) string { return "" }}
		_, err := c.Connect(context.Background(), Cursor)
		if got := errors.Is(err, ErrExeNotInstalled); got != bad {
			t.Errorf("%s: err = %v", exe, err)
		}
	}
}
