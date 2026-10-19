package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// AI client ids ("Conectar a IA").
const (
	ClaudeDesktop = "claude-desktop"
	ClaudeCode    = "claude-code"
	Cursor        = "cursor"
)

// ServerName is the key of the LiteZap entry under mcpServers.
const ServerName = "litezap"

// ErrMalformedConfig wraps a client config file that is not a JSON object (or whose
// mcpServers is not an object). Such a file is never touched.
var ErrMalformedConfig = errors.New("arquivo de configuração inválido (não é um JSON válido); corrija ou apague-o e tente de novo")

// ErrExeNotInstalled means the app runs from a place that will not exist later (the
// mounted .dmg, the random folder macOS uses for apps opened from Downloads, a zip opened
// without extracting, a temp folder).
var ErrExeNotInstalled = errors.New("o LiteZap está rodando de um lugar temporário (de dentro do .dmg ou do .zip, ou de uma pasta temporária): " +
	"mova-o para uma pasta fixa (Aplicativos no macOS, Documentos no Windows), abra-o de lá e conecte de novo")

// AIClient is one entry of the "Conectar a IA" menu.
type AIClient struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AIClients lists the supported clients in menu order.
func AIClients() []AIClient {
	return []AIClient{
		{ClaudeDesktop, "Claude Desktop"},
		{ClaudeCode, "Claude Code"},
		{Cursor, "Cursor"},
	}
}

// ClientName is the display name of a client id ("" if unknown).
func ClientName(id string) string {
	for _, c := range AIClients() {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// RestartHint tells the user what to do after connecting id.
func RestartHint(id string) string {
	switch id {
	case ClaudeCode:
		return "Abra uma nova sessão do Claude Code para ver as ferramentas do LiteZap."
	case Cursor:
		return "Reinicie o Cursor para ver as ferramentas do LiteZap."
	default:
		return "Reinicie o Claude Desktop para ver as ferramentas do LiteZap."
	}
}

// Connector writes and removes the LiteZap MCP entry in the config of AI clients. The
// entry runs the app's own executable with args ["mcp"] (see `litezap-app mcp`), so no
// token is ever copied into a client config.
type Connector struct {
	// Exe is the absolute path of the app executable the entry points to.
	Exe string
	// Env goes into the entry's "env" (e.g. LITEZAP_APP_DIR when the app runs with one).
	Env map[string]string

	GOOS   string
	Home   string
	Getenv func(string) string
	// LookPath finds the claude CLI; Run runs it. nil LookPath disables the CLI.
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewConnector returns a Connector for this machine and user.
func NewConnector(exe string, env map[string]string) (*Connector, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return &Connector{Exe: exe, Env: env, GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv,
		LookPath: exec.LookPath, Run: runCLI}, nil
}

// runCLI runs the claude CLI. Apps opened from Finder get a minimal PATH, and the npm
// install of claude is a `#!/usr/bin/env node` script: put its folder and the usual
// install places first so node is found.
func runCLI(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	path := []string{filepath.Dir(name)}
	if runtime.GOOS != "windows" {
		path = append(path, "/opt/homebrew/bin", "/usr/local/bin")
	}
	path = append(path, os.Getenv("PATH"))
	cmd.Env = append(os.Environ(), "PATH="+strings.Join(path, string(os.PathListSeparator)))
	return cmd.CombinedOutput()
}

// AIStatus is the state of one client.
type AIStatus struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is the config file read (and written when not using the claude CLI).
	Path string `json:"path"`
	// Installed is a best guess that the client exists on this machine.
	Installed bool `json:"installed"`
	// Connected: the entry exists and runs this executable with ["mcp"].
	Connected bool `json:"connected"`
	// Present: a "litezap" entry exists (it may point to another executable).
	Present bool `json:"present"`
	// Error is set when the config could not be read (e.g. malformed JSON).
	Error string `json:"error,omitempty"`
}

// AIResult describes what Connect or Disconnect did.
type AIResult struct {
	Path string `json:"path"`
	// Backup is the copy of the original file made before the first write ("" if none).
	Backup string `json:"backup,omitempty"`
	// Changed is false when there was nothing to do.
	Changed bool `json:"changed"`
	// CLI is true when the claude CLI did the change.
	CLI bool `json:"cli"`
}

// ConfigPath is the config file of client id.
func (c *Connector) ConfigPath(id string) (string, error) {
	switch id {
	case ClaudeDesktop:
		switch c.GOOS {
		case "darwin":
			return filepath.Join(c.Home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
		case "windows":
			if d := c.Getenv("APPDATA"); d != "" {
				return filepath.Join(d, "Claude", "claude_desktop_config.json"), nil
			}
			return filepath.Join(c.Home, "AppData", "Roaming", "Claude", "claude_desktop_config.json"), nil
		default:
			base := filepath.Join(c.Home, ".config")
			if d := c.Getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
				base = d
			}
			return filepath.Join(base, "Claude", "claude_desktop_config.json"), nil
		}
	case ClaudeCode:
		if d := c.Getenv("CLAUDE_CONFIG_DIR"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, ".claude.json"), nil
		}
		return filepath.Join(c.Home, ".claude.json"), nil
	case Cursor:
		return filepath.Join(c.Home, ".cursor", "mcp.json"), nil
	}
	return "", fmt.Errorf("cliente de IA desconhecido: %q", id)
}

// ExeProblem returns ErrExeNotInstalled when Exe is in a place that goes away.
func (c *Connector) ExeProblem() error {
	if temporaryExe(c.GOOS, c.Exe, c.Getenv) {
		return ErrExeNotInstalled
	}
	return nil
}

func temporaryExe(goos, exe string, getenv func(string) string) bool {
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	lower := strings.ToLower(exe)
	// A component like "LiteZap-windows.zip" means it runs from inside an archive.
	for _, part := range strings.Split(lower, sep) {
		if strings.HasSuffix(part, ".zip") || strings.HasSuffix(part, ".tar.gz") || strings.HasSuffix(part, ".dmg") {
			return true
		}
	}
	under := func(dir string) bool {
		dir = strings.ToLower(strings.TrimRight(dir, `/\`))
		return dir != "" && strings.HasPrefix(lower, dir+sep)
	}
	switch goos {
	case "darwin":
		return strings.Contains(exe, "/AppTranslocation/") || strings.HasPrefix(exe, "/Volumes/") ||
			strings.HasPrefix(exe, "/private/var/folders/") || strings.HasPrefix(exe, "/var/folders/") ||
			strings.HasPrefix(exe, "/tmp/") || strings.HasPrefix(exe, "/private/tmp/")
	case "windows":
		// Explorer runs an .exe opened inside a zip from %TEMP%\Temp1_<name>.zip\.
		return under(getenv("TEMP")) || under(getenv("TMP")) ||
			(getenv("LOCALAPPDATA") != "" && under(getenv("LOCALAPPDATA")+`\Temp`)) ||
			strings.Contains(lower, `\appdata\local\temp\`)
	default:
		return strings.HasPrefix(exe, "/tmp/") || under(getenv("TMPDIR"))
	}
}

// Statuses returns the state of every client.
func (c *Connector) Statuses() []AIStatus {
	out := make([]AIStatus, 0, 3)
	for _, cl := range AIClients() {
		out = append(out, c.Status(cl.ID))
	}
	return out
}

// Status reads the state of client id.
func (c *Connector) Status(id string) AIStatus {
	st := AIStatus{ID: id, Name: ClientName(id)}
	p, err := c.ConfigPath(id)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Path = p
	st.Installed = c.installed(id, p)
	f, err := readClientConfig(p)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Present, st.Connected = c.entryState(f.servers)
	return st
}

func (c *Connector) installed(id, path string) bool {
	if id == ClaudeCode {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		return c.claudeCLI() != ""
	}
	_, err := os.Stat(filepath.Dir(path)) // the client creates its folder on first run
	return err == nil
}

func (c *Connector) entryState(servers map[string]json.RawMessage) (present, connected bool) {
	raw, ok := servers[ServerName]
	if !ok {
		return false, false
	}
	var e struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return true, false
	}
	same := e.Command == c.Exe || c.GOOS == "windows" && strings.EqualFold(e.Command, c.Exe)
	for k, v := range c.Env {
		if e.Env[k] != v {
			same = false
		}
	}
	return true, same && slices.Equal(e.Args, []string{"mcp"})
}

func (c *Connector) entry(id string) map[string]any {
	e := map[string]any{"command": c.Exe, "args": []string{"mcp"}}
	if id == ClaudeCode {
		e["type"] = "stdio" // the shape `claude mcp add` writes
	}
	if len(c.Env) > 0 {
		e["env"] = c.Env
	} else if id == ClaudeCode {
		e["env"] = map[string]string{}
	}
	return e
}

// Connect adds (or fixes) the LiteZap entry of client id. Other servers and keys are
// kept byte for byte; the original file is backed up once, before the first change; a
// malformed file is refused with ErrMalformedConfig.
//
// Claude Code goes through `claude mcp add` when the CLI is found and there is no entry
// yet (the CLI knows how to write its own file); a stale entry is fixed by editing the
// file, so a failing add can never leave the user without the old entry.
func (c *Connector) Connect(ctx context.Context, id string) (AIResult, error) {
	if err := c.ExeProblem(); err != nil {
		return AIResult{}, err
	}
	p, err := c.ConfigPath(id)
	if err != nil {
		return AIResult{}, err
	}
	target, err := resolveTarget(p)
	if err != nil {
		return AIResult{Path: p}, err
	}
	f, err := readClientConfig(target)
	if err != nil {
		return AIResult{Path: p}, err
	}
	present, connected := c.entryState(f.servers)
	if connected {
		return AIResult{Path: p}, nil
	}
	if id == ClaudeCode && !present {
		if cli := c.claudeCLI(); cli != "" {
			bak, err := backupConfig(target, f)
			if err != nil {
				return AIResult{Path: p}, err
			}
			if err := c.runClaude(ctx, cli, c.addArgs()...); err == nil {
				return AIResult{Path: p, Backup: bak, Changed: true, CLI: true}, nil
			}
			// A broken CLI install: fall back to editing the file.
		}
	}
	return c.edit(p, target, c.entry(id), false)
}

// Disconnect removes the LiteZap entry of client id (nothing else).
func (c *Connector) Disconnect(ctx context.Context, id string) (AIResult, error) {
	p, err := c.ConfigPath(id)
	if err != nil {
		return AIResult{}, err
	}
	target, err := resolveTarget(p)
	if err != nil {
		return AIResult{Path: p}, err
	}
	f, err := readClientConfig(target)
	if err != nil {
		return AIResult{Path: p}, err
	}
	if _, ok := f.servers[ServerName]; !ok {
		return AIResult{Path: p}, nil
	}
	if id == ClaudeCode {
		if cli := c.claudeCLI(); cli != "" {
			bak, err := backupConfig(target, f)
			if err != nil {
				return AIResult{Path: p}, err
			}
			if err := c.runClaude(ctx, cli, "mcp", "remove", "--scope", "user", ServerName); err == nil {
				return AIResult{Path: p, Backup: bak, Changed: true, CLI: true}, nil
			}
		}
	}
	return c.edit(p, target, nil, true)
}

// addArgs is `claude mcp add --scope user litezap [-e K=V ...] -- <exe> mcp`. The name
// goes before -e: -e takes several values and would swallow it.
func (c *Connector) addArgs() []string {
	args := []string{"mcp", "add", "--scope", "user", ServerName}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+c.Env[k])
	}
	return append(args, "--", c.Exe, "mcp")
}

// runClaude runs the claude CLI and turns a failure into an error with its output.
func (c *Connector) runClaude(ctx context.Context, cli string, args ...string) error {
	out, err := c.Run(ctx, cli, args...)
	if err != nil {
		return fmt.Errorf("claude %s: %v: %s", strings.Join(args[:2], " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// claudeCLI finds the claude CLI: PATH first, then the usual install places (an app
// opened from Finder or the Dock does not see the shell's PATH).
func (c *Connector) claudeCLI() string {
	if c.LookPath == nil {
		return ""
	}
	if p, err := c.LookPath("claude"); err == nil {
		return p
	}
	var cands []string
	if c.GOOS == "windows" {
		if d := c.Getenv("APPDATA"); d != "" {
			cands = append(cands, filepath.Join(d, "npm", "claude.cmd"))
		}
		cands = append(cands, filepath.Join(c.Home, ".local", "bin", "claude.exe"))
	} else {
		cands = append(cands,
			filepath.Join(c.Home, ".local", "bin", "claude"),
			filepath.Join(c.Home, ".claude", "local", "claude"),
			"/opt/homebrew/bin/claude",
			"/usr/local/bin/claude")
	}
	for _, p := range cands {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// clientConfig is a client config file as read: its bytes (edited by splicing) and the
// parsed mcpServers (to know whether LiteZap is there).
type clientConfig struct {
	raw     []byte // file content as read (nil: no file)
	mode    os.FileMode
	modTime time.Time
	size    int64
	servers map[string]json.RawMessage
}

func readClientConfig(path string) (clientConfig, error) {
	f := clientConfig{mode: 0o600, servers: map[string]json.RawMessage{}}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	f.raw, f.mode, f.modTime, f.size = b, fi.Mode().Perm(), fi.ModTime(), fi.Size()
	if len(bytes.TrimSpace(b)) == 0 {
		return f, nil // an empty file is an empty config
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil || root == nil {
		return f, malformed(path, err)
	}
	if s, ok := root["mcpServers"]; ok && !bytes.Equal(bytes.TrimSpace(s), []byte("null")) {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(s, &servers); err != nil {
			return f, malformed(path, fmt.Errorf("mcpServers não é um objeto: %v", err))
		}
		f.servers = servers
	}
	return f, nil
}

// unchanged reports whether path still is what f read (size, mtime and content).
func (f clientConfig) unchanged(path string) bool {
	fi, err := os.Stat(path)
	if f.raw == nil {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil || fi.Size() != f.size || !fi.ModTime().Equal(f.modTime) {
		return false
	}
	now, err := os.ReadFile(path)
	return err == nil && bytes.Equal(now, f.raw)
}

func malformed(path string, err error) error {
	if err == nil {
		err = errors.New("o conteúdo não é um objeto JSON")
	}
	return fmt.Errorf("%s: %w (%v)", path, ErrMalformedConfig, err)
}

// resolveTarget follows a symlinked config (dotfiles) so the link stays a link.
func resolveTarget(path string) (string, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return filepath.EvalSymlinks(path)
	}
	return path, nil
}

func backupConfig(target string, f clientConfig) (string, error) {
	if f.raw == nil {
		return "", nil
	}
	return backupOnce(target, f.raw, f.mode)
}

// edit sets (or, with remove, deletes) mcpServers.litezap in the file and writes it
// atomically. It retries when the file changed while it worked (Claude Code rewrites
// ~/.claude.json often).
func (c *Connector) edit(path, target string, entry any, remove bool) (AIResult, error) {
	res := AIResult{Path: path}
	for attempt := 0; ; attempt++ {
		f, err := readClientConfig(target)
		if err != nil {
			return res, err
		}
		out, changed, err := spliceServer(f.raw, ServerName, entry, remove)
		if err != nil {
			return res, malformed(target, err)
		}
		if !changed {
			return res, nil
		}
		if res.Backup == "" {
			if res.Backup, err = backupConfig(target, f); err != nil {
				return res, err
			}
		}
		err = writeAtomic(target, out, f.mode, func() bool { return f.unchanged(target) })
		if errors.Is(err, errChanged) && attempt < 3 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err != nil {
			return res, err
		}
		res.Changed = true
		return res, nil
	}
}

// BackupSuffix is appended to a client config to name the copy of the original.
const BackupSuffix = ".litezap.bak"

// backupOnce copies the original content next to path, only if no backup exists yet: the
// first original is the one worth keeping (later changes are not backed up again).
func backupOnce(path string, content []byte, mode os.FileMode) (string, error) {
	bak := path + BackupSuffix
	if _, err := os.Lstat(bak); err == nil {
		return "", nil
	}
	f, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(bak)
		return "", fmt.Errorf("backup: %w", errors.Join(werr, cerr))
	}
	return bak, nil
}

var errChanged = errors.New("file changed while editing")

// writeAtomic writes content to a temp file in the same folder and renames it over path,
// so a reader never sees a half-written file. unchanged is checked right before the
// rename.
func writeAtomic(path string, content []byte, mode os.FileMode, unchanged func() bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".litezap-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	_, werr := tmp.Write(content)
	if werr == nil {
		werr = tmp.Chmod(mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if unchanged != nil && !unchanged() {
		return errChanged
	}
	return os.Rename(name, path)
}
