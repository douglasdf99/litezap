package desktop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/config"
)

// ErrNotSetUp means the tray app never ran with this app folder: there is no admin
// token yet, so `litezap-app mcp` has nothing to authenticate with.
var ErrNotSetUp = errors.New("o LiteZap ainda não foi aberto neste computador: abra o app LiteZap uma vez e tente de novo")

// NotRunningMessage is what the MCP tools answer while the tray app is closed.
const NotRunningMessage = "o LiteZap não está aberto neste computador: abra o app LiteZap (ícone na barra de menus/bandeja) e tente de novo"

// Engine is how a local client (the `mcp` subcommand) reaches the engine run by the tray
// app with app folder dir.
type Engine struct {
	URL   string // http://127.0.0.1:8787
	Token string // admin token
}

// BaseConfig is the configuration the tray app starts from: defaults with data_dir in
// the app folder. litezap.yaml and LITEZAP_* variables apply on top of it.
func BaseConfig(dir string) config.Config {
	c := config.Default()
	c.DataDir = filepath.Join(dir, "data")
	return c
}

// ConfigPath is the optional litezap.yaml of the tray app.
func ConfigPath(dir string) string { return filepath.Join(dir, "litezap.yaml") }

// LocalEngine resolves the engine of the app folder dir the same way the tray app does
// (litezap.yaml, LITEZAP_* variables, data/admin_token). It never creates a token.
func LocalEngine(dir string) (Engine, error) {
	cfg, err := config.LoadWith(ConfigPath(dir), BaseConfig(dir))
	if err != nil {
		return Engine{}, err
	}
	tok := cfg.AdminToken
	if tok == "" {
		b, err := os.ReadFile(filepath.Join(cfg.DataDir, "admin_token"))
		if errors.Is(err, os.ErrNotExist) {
			return Engine{}, ErrNotSetUp
		}
		if err != nil {
			return Engine{}, fmt.Errorf("ler o token admin: %w", err)
		}
		tok = strings.TrimSpace(string(b))
		if tok == "" {
			return Engine{}, ErrNotSetUp
		}
	}
	u, err := LoopbackURL(cfg.ListenAddr)
	if err != nil {
		return Engine{}, err
	}
	return Engine{URL: u, Token: tok}, nil
}

// LoopbackURL turns a listen address into a loopback URL a local client can dial (an
// unspecified host such as 0.0.0.0 or "" becomes 127.0.0.1). Any other host is refused:
// the admin token would travel in cleartext to another machine.
func LoopbackURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen_addr %q inválido: %w", addr, err)
	}
	ip := net.ParseIP(host)
	switch {
	case host == "" || ip != nil && ip.IsUnspecified():
		host = "127.0.0.1"
	case strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback():
	default:
		return "", fmt.Errorf("listen_addr %q não é local (loopback): o MCP do app só fala com o motor neste computador, "+
			"para não mandar o token admin sem criptografia pela rede; use listen_addr 127.0.0.1:<porta>", addr)
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// Running reports whether the engine answers /healthz within timeout.
func (e Engine) Running(ctx context.Context, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL+"/healthz", nil)
	if err != nil {
		return false
	}
	// /healthz may require the admin token when the listener is not loopback.
	req.Header.Set("Authorization", "Bearer "+e.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
