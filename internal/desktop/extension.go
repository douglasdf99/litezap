package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/douglasdf99/litezap/internal/mcpb"
)

// ErrNoExtension means this build of the app ships without LiteZap.mcpb (a development
// build): only the config file option is available.
var ErrNoExtension = errors.New("esta versão do LiteZap não traz a extensão do Claude Desktop (LiteZap.mcpb): " +
	"use \"Configurar manualmente\" ou baixe a extensão em https://litezap.com/docs/baixar/")

// BundledExtension is where the app ships LiteZap.mcpb next to the executable exe:
// LiteZap.app/Contents/Resources on macOS, the folder of the executable elsewhere.
func BundledExtension(goos, exe string) string {
	if goos == "darwin" {
		return filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources", mcpb.FileName)
	}
	return filepath.Join(filepath.Dir(exe), mcpb.FileName)
}

// ExtensionAvailable reports whether the app ships the Claude Desktop extension.
func (c *Connector) ExtensionAvailable() bool {
	if c.Extension == "" {
		return false
	}
	fi, err := os.Stat(c.Extension)
	return err == nil && fi.Mode().IsRegular()
}

// ExtensionResult describes what InstallExtension did.
type ExtensionResult struct {
	// Path of the .mcpb handed to Claude Desktop.
	Path string `json:"path"`
	// Running: Claude Desktop was open (it shows the install dialog right away).
	Running bool `json:"running"`
	// ManualRemoved: the manual "litezap" entry of claude_desktop_config.json was removed
	// (with the extension it would show every tool twice); Hint says how to finish when
	// Claude is open. ManualError is why that removal failed ("" if it did not).
	ManualRemoved bool   `json:"manual_removed,omitempty"`
	Hint          string `json:"hint,omitempty"`
	ManualError   string `json:"manual_error,omitempty"`
}

// InstallExtension writes a copy of the bundled extension that points at this executable
// (LITEZAP_APP_EXE, plus Env) into the user cache folder and opens it: Claude Desktop
// shows its install dialog. A manual LiteZap entry in claude_desktop_config.json is then
// removed like Disconnect does (with the guard when Claude is open), so the tools do not
// show up twice ("litezap" and "LiteZap (WhatsApp)").
func (c *Connector) InstallExtension() (ExtensionResult, error) {
	if err := c.ExeProblem(); err != nil {
		return ExtensionResult{}, err
	}
	if !c.ExtensionAvailable() {
		return ExtensionResult{}, ErrNoExtension
	}
	src, err := os.ReadFile(c.Extension)
	if err != nil {
		return ExtensionResult{}, err
	}
	env := map[string]string{mcpb.EnvAppExe: c.Exe}
	for k, v := range c.Env {
		env[k] = v
	}
	out, err := mcpb.Customize(src, env)
	if err != nil {
		return ExtensionResult{}, err
	}
	dir := c.CacheDir
	if dir == "" {
		// Never the shared temp folder: another user could plant the file there first.
		base, err := os.UserCacheDir()
		if err != nil {
			return ExtensionResult{}, fmt.Errorf("pasta de cache do usuário: %w", err)
		}
		dir = filepath.Join(base, "LiteZap")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ExtensionResult{}, err
	}
	p := filepath.Join(dir, mcpb.FileName)
	if err := writeAtomic(p, out, 0o600, nil); err != nil {
		return ExtensionResult{}, err
	}
	res := ExtensionResult{Path: p, Running: c.clientRunning(ClaudeDesktop)}
	open := c.Open
	if open == nil {
		open = Open
	}
	if err := open(p); err != nil {
		return res, fmt.Errorf("abrir %s: %w (abra o arquivo com o Claude Desktop)", p, err)
	}
	if !c.Status(ClaudeDesktop).Present {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := c.Disconnect(ctx, ClaudeDesktop)
	if c.Log != nil {
		c.Log("removed the manual Claude Desktop entry after installing the extension", "changed", d.Changed, "running", d.Running, "err", err)
	}
	if err != nil {
		res.ManualError = err.Error()
		return res, nil
	}
	res.ManualRemoved, res.Hint = d.Changed, d.Hint
	return res, nil
}

// extensionInstalled reports whether Claude Desktop has an extension named "litezap"
// installed (best effort: <config dir>/Claude Extensions/*/manifest.json).
func (c *Connector) extensionInstalled() bool {
	p, err := c.ConfigPath(ClaudeDesktop)
	if err != nil {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "Claude Extensions", "*", "manifest.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var man struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &man) == nil && man.Name == "litezap" {
			return true
		}
	}
	return false
}
