package mcpb

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// FileName is the name of the extension file (release asset and app resource).
const FileName = "LiteZap.mcpb"

// ManifestVersion is the MCPB manifest spec this package writes
// (github.com/modelcontextprotocol/mcpb, MANIFEST.md).
const ManifestVersion = "0.3"

// Shim paths inside the bundle. On Windows the client runs the .exe override.
const (
	ShimPath    = "server/litezap-mcpb"
	ShimPathExe = "server/litezap-mcpb.exe"
	IconPath    = "icon.png"
)

// Tool is one entry of manifest.tools.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Manifest is the manifest.json of the extension (the fields LiteZap uses).
type Manifest struct {
	ManifestVersion string         `json:"manifest_version"`
	Name            string         `json:"name"`
	DisplayName     string         `json:"display_name"`
	Version         string         `json:"version"`
	Description     string         `json:"description"`
	LongDescription string         `json:"long_description,omitempty"`
	Author          Author         `json:"author"`
	Homepage        string         `json:"homepage,omitempty"`
	Documentation   string         `json:"documentation,omitempty"`
	Support         string         `json:"support,omitempty"`
	Icon            string         `json:"icon,omitempty"`
	Server          Server         `json:"server"`
	Tools           []Tool         `json:"tools,omitempty"`
	ToolsGenerated  bool           `json:"tools_generated,omitempty"`
	Keywords        []string       `json:"keywords,omitempty"`
	License         string         `json:"license,omitempty"`
	Compatibility   *Compatibility `json:"compatibility,omitempty"`
}

type Author struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type Server struct {
	Type       string    `json:"type"`
	EntryPoint string    `json:"entry_point"`
	MCPConfig  MCPConfig `json:"mcp_config"`
}

type MCPConfig struct {
	Command           string                    `json:"command"`
	Args              []string                  `json:"args"`
	Env               map[string]string         `json:"env"`
	PlatformOverrides map[string]PlatformConfig `json:"platform_overrides,omitempty"`
}

type PlatformConfig struct {
	Command string `json:"command,omitempty"`
}

type Compatibility struct {
	ClaudeDesktop string   `json:"claude_desktop,omitempty"`
	Platforms     []string `json:"platforms,omitempty"`
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// SemVer turns a build version (v0.1.0, v0.1.0-rc.1, dev) into the semver the manifest
// needs (0.1.0, 0.1.0-rc.1, 0.0.0-dev).
func SemVer(v string) string {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if semverRe.MatchString(s) {
		return s
	}
	tag := strings.Trim(regexp.MustCompile(`[^0-9A-Za-z-]+`).ReplaceAllString(s, "-"), "-")
	if tag == "" {
		tag = "dev"
	}
	return "0.0.0-" + tag
}

// NewManifest describes the LiteZap extension. platforms are MCPB platform names
// (darwin, win32); the shim for win32 is ShimPathExe.
func NewManifest(version string, platforms []string, tools []Tool) Manifest {
	m := Manifest{
		ManifestVersion: ManifestVersion,
		Name:            "litezap",
		DisplayName:     "LiteZap (WhatsApp)",
		Version:         SemVer(version),
		Description:     "Use o seu WhatsApp pelo Claude: leia conversas, envie mensagens, mídias e gerencie grupos pelo app LiteZap neste computador.",
		LongDescription: "Conecta o Claude ao app **LiteZap** instalado neste computador. As ferramentas falam com o WhatsApp " +
			"pelo LiteZap aberto (ícone na barra de menus ou na bandeja), sem copiar tokens: a extensão encontra o app e usa " +
			"a sessão dele.\n\nO LiteZap precisa estar instalado e aberto. Baixe em https://litezap.com/docs/baixar/.",
		Author:        Author{Name: "LiteZap", URL: "https://litezap.com"},
		Homepage:      "https://litezap.com",
		Documentation: "https://litezap.com/docs/app-local/",
		Support:       "https://github.com/douglasdf99/litezap/issues",
		Icon:          IconPath,
		Server: Server{
			Type:       "binary",
			EntryPoint: ShimPath,
			MCPConfig: MCPConfig{
				Command: "${__dirname}/" + ShimPath,
				Args:    []string{},
				Env:     map[string]string{},
			},
		},
		Tools:          tools,
		ToolsGenerated: true,
		Keywords:       []string{"whatsapp", "litezap", "mensagens", "chat"},
		License:        "Apache-2.0",
		Compatibility:  &Compatibility{ClaudeDesktop: ">=0.10.0", Platforms: platforms},
	}
	if slices.Contains(platforms, "win32") {
		m.Server.MCPConfig.PlatformOverrides = map[string]PlatformConfig{"win32": {Command: "${__dirname}/" + ShimPathExe}}
	}
	return m
}

// File is one file of the bundle.
type File struct {
	Name string
	Data []byte
	Exec bool
}

// Pack writes the bundle (a zip with manifest.json first) to w.
func Pack(w io.Writer, m Manifest, files []File) error {
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeZip(w, append([]File{{Name: "manifest.json", Data: append(mb, '\n')}}, files...))
}

func writeZip(w io.Writer, files []File) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		if f.Name == "" || path.IsAbs(f.Name) || strings.Contains(f.Name, "..") || strings.Contains(f.Name, `\`) {
			return fmt.Errorf("mcpb: bad file name %q", f.Name)
		}
		hdr := &zip.FileHeader{Name: f.Name, Method: zip.Deflate}
		mode := fs.FileMode(0o644)
		if f.Exec {
			mode = 0o755 // kept when the client unpacks (Unix permission bits in the zip)
		}
		hdr.SetMode(mode)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// ReadManifest returns the manifest.json of a bundle as a generic object.
func ReadManifest(bundle []byte) (map[string]any, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return nil, fmt.Errorf("mcpb: %w", err)
	}
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			b, err := readZipFile(f)
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return nil, fmt.Errorf("mcpb: manifest.json: %w", err)
			}
			return m, nil
		}
	}
	return nil, errors.New("mcpb: no manifest.json")
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// Customize returns a copy of bundle whose server.mcp_config.env has env added (e.g.
// LITEZAP_APP_EXE, the executable of the app that generated it). Other files and
// manifest fields are kept as they are.
func Customize(bundle []byte, env map[string]string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return nil, fmt.Errorf("mcpb: %w", err)
	}
	var files []File
	found := false
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		b, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		if f.Name == "manifest.json" {
			found = true
			if b, err = withEnv(b, env); err != nil {
				return nil, err
			}
		}
		files = append(files, File{Name: f.Name, Data: b, Exec: f.Mode().Perm()&0o111 != 0})
	}
	if !found {
		return nil, errors.New("mcpb: no manifest.json")
	}
	var out bytes.Buffer
	if err := writeZip(&out, files); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func withEnv(manifest []byte, env map[string]string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("mcpb: manifest.json: %w", err)
	}
	server, _ := m["server"].(map[string]any)
	cfg, _ := server["mcp_config"].(map[string]any)
	if cfg == nil {
		return nil, errors.New("mcpb: manifest.json has no server.mcp_config")
	}
	e, _ := cfg["env"].(map[string]any)
	if e == nil {
		e = map[string]any{}
	}
	for k, v := range env {
		e[k] = v
	}
	cfg["env"] = e
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
