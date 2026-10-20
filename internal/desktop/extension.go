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
	"use \"Avançado > Configurar manualmente\" ou baixe a extensão em https://litezap.com/docs/baixar/")

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
	// Hint says what happens next (ExtensionReadyHint: no restart).
	Hint string `json:"hint"`
	// ManualRemoved: the manual "litezap" entry of claude_desktop_config.json was removed
	// (with the extension it would show every tool twice); ManualHint is set when Claude
	// was open (the old copy may show until it next opens). ManualError is why that
	// removal failed ("" if it did not).
	ManualRemoved bool   `json:"manual_removed,omitempty"`
	ManualHint    string `json:"manual_hint,omitempty"`
	ManualError   string `json:"manual_error,omitempty"`
}

// InstallExtension writes a copy of the bundled extension that points at this executable
// (LITEZAP_APP_EXE, plus Env) into the user cache folder and opens it: Claude Desktop
// shows its install dialog, and once the user clicks Instalar it starts the extension with
// no restart (see ExtensionReadyHint). A manual LiteZap entry in claude_desktop_config.json is then
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
	res := ExtensionResult{Path: p, Running: c.clientRunning(ClaudeDesktop), Hint: ExtensionReadyHint}
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
	res.ManualRemoved = d.Changed
	if d.Changed && d.Running {
		res.ManualHint = manualRemovedHint
	}
	return res, nil
}

// ExtensionID is the folder name Claude Desktop gives the LiteZap extension: "local.mcpb."
// + the slug of manifest.author.name + "." + manifest.name, for an unsigned .mcpb installed
// from a file. Seen on macOS (Claude Desktop, 2026-10-19):
// ~/Library/Application Support/Claude/Claude Extensions/local.mcpb.litezap.litezap/ and,
// for its on/off switch, Claude Extensions Settings/local.mcpb.litezap.litezap.json
// ({"isEnabled": true}).
const ExtensionID = "local.mcpb.litezap.litezap"

// ExtensionReadyHint is what to tell the user after handing the extension to Claude
// Desktop. No restart: Claude Desktop enables an extension installed from its dialog and
// starts its server right away. Verified on macOS with the Claude Desktop log
// (~/Library/Logs/Claude/main.log): "Successfully installed extension
// local.mcpb.litezap.litezap", "Enabling extension ... after installation", "Launching
// MCP Server: LiteZap (WhatsApp)" and "Connected to LiteZap (WhatsApp) (40 tools)" all
// within one second, with the app still running (it quit 40 s later, by hand). The MCPB
// docs (github.com/modelcontextprotocol/mcpb) do not promise it, so the copy only says it
// may take a few seconds.
const ExtensionReadyHint = "Depois de clicar em Instalar, as ferramentas do LiteZap aparecem no Claude sem reiniciar " +
	"(pode levar alguns segundos para aparecer)."

// manualRemovedHint replaces the restart hint of Disconnect after the extension took over:
// the extension already works, the old manual entry only goes away for good the next time
// Claude Desktop opens (meanwhile the tools may show twice, which is harmless).
const manualRemovedHint = "Se nesta sessão do Claude as ferramentas aparecerem em dobro, tudo bem: a cópia antiga some " +
	"sozinha na próxima vez que o Claude abrir."

// ExtensionState is what Claude Desktop has of the LiteZap extension (best effort, from
// its files: nothing here talks to Claude).
type ExtensionState struct {
	Installed bool
	// Disabled: installed but switched off in Claude Desktop (Configurações > Extensões).
	Disabled bool
}

// extensionsDir is <Claude Desktop config dir>/Claude Extensions.
func (c *Connector) extensionsDir() (string, error) {
	p, err := c.ConfigPath(ClaudeDesktop)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "Claude Extensions"), nil
}

// ExtensionState reads whether Claude Desktop has the LiteZap extension: the folder
// ExtensionID with a manifest.json first, then any extension folder whose manifest is
// named "litezap" (a future Claude version or a signed copy may use another id). The
// on/off switch is Claude Extensions Settings/<id>.json.
func (c *Connector) ExtensionState() ExtensionState {
	dir, err := c.extensionsDir()
	if err != nil {
		return ExtensionState{}
	}
	id := ""
	if fi, err := os.Stat(filepath.Join(dir, ExtensionID, "manifest.json")); err == nil && fi.Mode().IsRegular() {
		id = ExtensionID
	} else {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "manifest.json"))
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			var man struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(b, &man) == nil && man.Name == "litezap" {
				id = filepath.Base(filepath.Dir(m))
				break
			}
		}
	}
	if id == "" {
		return ExtensionState{}
	}
	st := ExtensionState{Installed: true}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "Claude Extensions Settings", id+".json"))
	if err == nil {
		var set struct {
			IsEnabled *bool `json:"isEnabled"`
		}
		if json.Unmarshal(b, &set) == nil && set.IsEnabled != nil && !*set.IsEnabled {
			st.Disabled = true
		}
	}
	return st
}
