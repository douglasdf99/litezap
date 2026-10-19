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
	c.Env = map[string]string{"LITEZAP_APP_DIR": "/tmp/lz"}
	var calls [][]string
	c.LookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	record := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	c.Run = record
	p, _ := c.ConfigPath(ClaudeCode)
	orig := `{"numStartups": 3, "mcpServers": {"other": {"command": "x"}}}`
	writeFile(t, p, orig)
	res, err := c.Connect(context.Background(), ClaudeCode)
	if err != nil || !res.CLI || !res.Changed {
		t.Fatalf("connect: %+v, %v", res, err)
	}
	// The name must come before -e (it takes several values).
	want := [][]string{{"/usr/local/bin/claude", "mcp", "add", "--scope", "user", "litezap",
		"-e", "LITEZAP_APP_DIR=/tmp/lz", "--", testExe, "mcp"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %q\nwant  %q", calls, want)
	}
	// The CLI path backs the file up too.
	if res.Backup != p+BackupSuffix {
		t.Errorf("backup = %q", res.Backup)
	}
	if b, _ := os.ReadFile(p + BackupSuffix); string(b) != orig {
		t.Error("backup content differs")
	}

	// A failing CLI falls back to editing the file.
	calls = nil
	c.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("boom"), errors.New("exit 1") }
	if res, err := c.Connect(context.Background(), ClaudeCode); err != nil || res.CLI {
		t.Fatalf("fallback: %+v, %v", res, err)
	}
	if !c.Status(ClaudeCode).Connected {
		t.Error("fallback did not connect")
	}

	c.Run = record
	if res, err := c.Disconnect(context.Background(), ClaudeCode); err != nil || !res.CLI {
		t.Fatalf("disconnect: %+v, %v", res, err)
	}
	if len(calls) != 1 || calls[0][2] != "remove" {
		t.Errorf("disconnect calls = %q", calls)
	}
}

// A stale entry is fixed in the file, never with remove+add (a failed add would lose it).
func TestClaudeCodeStaleEntryUsesFile(t *testing.T) {
	c := testConnector(t)
	var calls int
	c.LookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	c.Run = func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }
	p, _ := c.ConfigPath(ClaudeCode)
	writeFile(t, p, `{"mcpServers":{"litezap":{"type":"stdio","command":"/old","args":["mcp"]}}}`)
	res, err := c.Connect(context.Background(), ClaudeCode)
	if err != nil || res.CLI || !res.Changed || calls != 0 {
		t.Fatalf("connect: %+v, %v, %d CLI calls", res, err, calls)
	}
	if !c.Status(ClaudeCode).Connected {
		t.Error("not connected")
	}
}

func TestEditPreservesBytesOutsideEntry(t *testing.T) {
	c := testConnector(t)
	p, _ := c.ConfigPath(ClaudeCode)
	// Unsorted keys, a duplicate key, tabs, unicode escapes and HTML characters.
	orig := "{\n\t\"zeta\": 1,\n\t\"alpha\": {\"b\": 2, \"a\": 1},\n\t\"dup\": 1,\n\t\"dup\": 2,\n" +
		"\t\"mcpServers\": {\n\t\t\"z-server\": {\"command\": \"<x>&\"},\n\t\t\"a-server\": {\"url\": \"\\u00e9\"}\n\t},\n" +
		"\t\"middle\": [3, 2, 1]\n}\n"
	writeFile(t, p, orig)
	if _, err := c.Connect(context.Background(), ClaudeCode); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	head := "{\n\t\"zeta\": 1,\n\t\"alpha\": {\"b\": 2, \"a\": 1},\n\t\"dup\": 1,\n\t\"dup\": 2,\n" +
		"\t\"mcpServers\": {\n\t\t\"z-server\": {\"command\": \"<x>&\"},\n\t\t\"a-server\": {\"url\": \"\\u00e9\"},\n\t\t\"litezap\": {"
	tail := "\n\t},\n\t\"middle\": [3, 2, 1]\n}\n"
	if !strings.HasPrefix(string(got), head) || !strings.HasSuffix(string(got), tail) {
		t.Fatalf("bytes outside the entry changed:\n%s", got)
	}
	if !c.Status(ClaudeCode).Connected {
		t.Fatal("not connected")
	}
	// Disconnect gives the original back, byte for byte.
	if _, err := c.Disconnect(context.Background(), ClaudeCode); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != orig {
		t.Errorf("after disconnect:\n%s\nwant\n%s", got, orig)
	}
}

func TestSpliceShapes(t *testing.T) {
	entry := map[string]any{"command": "/a", "args": []string{"mcp"}}
	cases := []struct{ name, in string }{
		{"compact", `{"a":1}`},
		{"no servers, indented", "{\n  \"a\": 1\n}\n"},
		{"empty object", `{}`},
		{"servers null", `{"mcpServers": null, "b": 2}`},
		{"servers empty", "{\n  \"mcpServers\": {}\n}"},
		{"litezap first of two", `{"mcpServers": {"litezap": {"command": "/old"}, "x": {}}}`},
		{"litezap duplicated", `{"mcpServers": {"litezap": {}, "x": {}, "litezap": {"command": "/old"}}}`},
	}
	for _, c := range cases {
		out, changed, err := spliceServer([]byte(c.in), "litezap", entry, false)
		if err != nil || !changed || !json.Valid(out) {
			t.Errorf("%s: set: %v %v\n%s", c.name, changed, err, out)
			continue
		}
		var m struct {
			MCPServers map[string]struct{ Command string } `json:"mcpServers"`
		}
		json.Unmarshal(out, &m)
		if m.MCPServers["litezap"].Command != "/a" {
			t.Errorf("%s: entry not set:\n%s", c.name, out)
		}
		back, changed, err := spliceServer(out, "litezap", nil, true)
		if err != nil || !changed || !json.Valid(back) || strings.Contains(string(back), "litezap") {
			t.Errorf("%s: remove: %v %v\n%s", c.name, changed, err, back)
		}
	}
	if _, changed, _ := spliceServer([]byte(`{"a":1}`), "litezap", nil, true); changed {
		t.Error("removing an absent entry reported a change")
	}
}

func TestUnchangedDetectsRewrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, p, `{"a":1}`)
	f, err := readClientConfig(p)
	if err != nil || !f.unchanged(p) {
		t.Fatalf("fresh read: %v", err)
	}
	writeFile(t, p, `{"a":2}`) // same size, new content
	if f.unchanged(p) {
		t.Error("rewrite not detected")
	}
}

func TestExeProblem(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	win := map[string]string{"TEMP": `C:\Users\ana\AppData\Local\Temp`, "LOCALAPPDATA": `C:\Users\ana\AppData\Local`}
	cases := []struct {
		goos, exe string
		env       map[string]string
		bad       bool
	}{
		{"darwin", testExe, nil, false},
		{"darwin", "/private/var/folders/x/AppTranslocation/ABC/d/LiteZap.app/Contents/MacOS/litezap-app", nil, true},
		{"darwin", "/Volumes/LiteZap/LiteZap.app/Contents/MacOS/litezap-app", nil, true},
		{"darwin", "/Users/ana/Downloads/LiteZap.app/Contents/MacOS/litezap-app", nil, false},
		{"windows", `C:\Users\ana\Documents\LiteZap\LiteZap.exe`, win, false},
		{"windows", `C:\Users\ana\Downloads\LiteZap-windows\LiteZap.exe`, win, false},
		{"windows", `C:\Users\ana\AppData\Local\Temp\Temp1_LiteZap-windows.zip\LiteZap.exe`, win, true},
		{"windows", `C:\Users\ana\Downloads\LiteZap-windows.zip\LiteZap.exe`, win, true},
		{"windows", `D:\tmp\x\LiteZap.exe`, map[string]string{"TEMP": `D:\tmp`}, true},
		{"linux", "/home/ana/LiteZap/litezap-app", nil, false},
		{"linux", "/tmp/LiteZap/litezap-app", nil, true},
	}
	for _, tc := range cases {
		c := &Connector{Exe: tc.exe, GOOS: tc.goos, Home: t.TempDir(), Getenv: env(tc.env)}
		if got := errors.Is(c.ExeProblem(), ErrExeNotInstalled); got != tc.bad {
			t.Errorf("%s %s: bad = %v, want %v", tc.goos, tc.exe, got, tc.bad)
		}
	}
	c := &Connector{Exe: "/Volumes/LiteZap/LiteZap.app/Contents/MacOS/litezap-app", GOOS: "darwin", Home: t.TempDir(), Getenv: env(nil)}
	if _, err := c.Connect(context.Background(), Cursor); !errors.Is(err, ErrExeNotInstalled) {
		t.Errorf("connect from the dmg: %v", err)
	}
}
