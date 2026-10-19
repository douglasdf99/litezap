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
// mounted .dmg, or the random folder macOS uses for apps opened from Downloads).
var ErrExeNotInstalled = errors.New("mova o LiteZap para a pasta Aplicativos, abra-o de lá e conecte de novo")

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
	if c.GOOS == "darwin" && (strings.Contains(c.Exe, "/AppTranslocation/") || strings.HasPrefix(c.Exe, "/Volumes/")) {
		return ErrExeNotInstalled
	}
	return nil
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
// kept; the original file is backed up before the first write; a malformed file is
// refused with ErrMalformedConfig.
func (c *Connector) Connect(ctx context.Context, id string) (AIResult, error) {
	if err := c.ExeProblem(); err != nil {
		return AIResult{}, err
	}
	p, err := c.ConfigPath(id)
	if err != nil {
		return AIResult{}, err
	}
	f, err := readClientConfig(p)
	if err != nil {
		return AIResult{Path: p}, err
	}
	present, connected := c.entryState(f.servers)
	if connected {
		return AIResult{Path: p}, nil
	}
	if id == ClaudeCode {
		if cli := c.claudeCLI(); cli != "" {
			if res, err := c.connectCLI(ctx, cli, p, present); err == nil {
				return res, nil
			}
			// A broken CLI install: fall back to editing the file.
		}
	}
	return c.edit(p, func(servers map[string]json.RawMessage) bool {
		b, _ := json.Marshal(c.entry(id))
		servers[ServerName] = b
		return true
	})
}

// Disconnect removes the LiteZap entry of client id (nothing else).
func (c *Connector) Disconnect(ctx context.Context, id string) (AIResult, error) {
	p, err := c.ConfigPath(id)
	if err != nil {
		return AIResult{}, err
	}
	f, err := readClientConfig(p)
	if err != nil {
		return AIResult{Path: p}, err
	}
	if _, ok := f.servers[ServerName]; !ok {
		return AIResult{Path: p}, nil
	}
	if id == ClaudeCode {
		if cli := c.claudeCLI(); cli != "" {
			if _, err := c.Run(ctx, cli, "mcp", "remove", "--scope", "user", ServerName); err == nil {
				return AIResult{Path: p, Changed: true, CLI: true}, nil
			}
		}
	}
	return c.edit(p, func(servers map[string]json.RawMessage) bool {
		if _, ok := servers[ServerName]; !ok {
			return false
		}
		delete(servers, ServerName)
		return true
	})
}

func (c *Connector) connectCLI(ctx context.Context, cli, path string, present bool) (AIResult, error) {
	if present {
		if out, err := c.Run(ctx, cli, "mcp", "remove", "--scope", "user", ServerName); err != nil {
			return AIResult{}, fmt.Errorf("claude mcp remove: %v: %s", err, bytes.TrimSpace(out))
		}
	}
	args := []string{"mcp", "add", "--scope", "user"}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+c.Env[k])
	}
	args = append(args, ServerName, "--", c.Exe, "mcp")
	if out, err := c.Run(ctx, cli, args...); err != nil {
		return AIResult{}, fmt.Errorf("claude mcp add: %v: %s", err, bytes.TrimSpace(out))
	}
	return AIResult{Path: path, Changed: true, CLI: true}, nil
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

// clientConfig is a parsed client config file. Values are kept as raw JSON so unknown
// keys and other servers are written back unchanged.
type clientConfig struct {
	raw     []byte // file content as read (nil: no file)
	mode    os.FileMode
	root    map[string]json.RawMessage
	servers map[string]json.RawMessage
}

func readClientConfig(path string) (clientConfig, error) {
	f := clientConfig{mode: 0o600, root: map[string]json.RawMessage{}, servers: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	f.raw = b
	if fi, err := os.Stat(path); err == nil {
		f.mode = fi.Mode().Perm()
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return f, nil // an empty file is an empty config
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil || root == nil {
		return f, malformed(path, err)
	}
	f.root = root
	if s, ok := root["mcpServers"]; ok && !bytes.Equal(bytes.TrimSpace(s), []byte("null")) {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(s, &servers); err != nil {
			return f, malformed(path, fmt.Errorf("mcpServers não é um objeto: %v", err))
		}
		f.servers = servers
	}
	return f, nil
}

func malformed(path string, err error) error {
	if err == nil {
		err = errors.New("o conteúdo não é um objeto JSON")
	}
	return fmt.Errorf("%s: %w (%v)", path, ErrMalformedConfig, err)
}

// edit applies change to mcpServers of path and writes the file atomically. It retries
// when the file changed while it worked (Claude Code rewrites ~/.claude.json often).
func (c *Connector) edit(path string, change func(map[string]json.RawMessage) bool) (AIResult, error) {
	res := AIResult{Path: path}
	target := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return res, err
		}
		target = real // keep a symlinked config (dotfiles) a symlink
	}
	for attempt := 0; ; attempt++ {
		f, err := readClientConfig(target)
		if err != nil {
			return res, err
		}
		if !change(f.servers) {
			return res, nil
		}
		sb, err := marshalJSON(f.servers)
		if err != nil {
			return res, err
		}
		f.root["mcpServers"] = sb
		out, err := marshalJSON(f.root)
		if err != nil {
			return res, err
		}
		out = append(out, '\n')
		if f.raw != nil && res.Backup == "" {
			bak, err := backupOnce(target, f.raw, f.mode)
			if err != nil {
				return res, err
			}
			res.Backup = bak
		}
		err = writeAtomic(target, out, f.mode, func() bool {
			now, err := os.ReadFile(target)
			if f.raw == nil {
				return errors.Is(err, os.ErrNotExist)
			}
			return err == nil && bytes.Equal(now, f.raw)
		})
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

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// BackupSuffix is appended to a client config to name the copy of the original.
const BackupSuffix = ".litezap.bak"

// backupOnce copies the original content next to path, only if no backup exists yet: the
// first original is the one worth keeping.
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
