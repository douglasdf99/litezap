// Package config loads LiteZap configuration from a YAML file and LITEZAP_* environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr     string `yaml:"listen_addr"`
	PublicURL      string `yaml:"public_url"`
	DataDir        string `yaml:"data_dir"`
	AdminToken     string `yaml:"admin_token"`
	WorkerMemoryMB int64  `yaml:"worker_memory_max_mb"`
}

func Default() Config {
	return Config{ListenAddr: "127.0.0.1:8787", DataDir: "data", WorkerMemoryMB: 512}
}

// Load reads path (a missing file means defaults) and applies env overrides.
// DataDir is always returned as an absolute path.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return cfg, fmt.Errorf("read config: %w", err)
		default:
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config: %w", err)
			}
		}
	}
	if v := os.Getenv("LITEZAP_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("LITEZAP_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := os.Getenv("LITEZAP_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LITEZAP_ADMIN_TOKEN"); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("LITEZAP_WORKER_MEMORY_MAX_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_WORKER_MEMORY_MAX_MB: %w", err)
		}
		cfg.WorkerMemoryMB = n
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return cfg, fmt.Errorf("data dir: %w", err)
	}
	cfg.DataDir = abs
	return cfg, nil
}

// EnsureAdminToken keeps a configured token, otherwise loads or creates DataDir/admin_token (0600).
func EnsureAdminToken(cfg *Config) error {
	if cfg.AdminToken != "" {
		return nil
	}
	p := filepath.Join(cfg.DataDir, "admin_token")
	b, err := os.ReadFile(p)
	if err == nil {
		cfg.AdminToken = strings.TrimSpace(string(b))
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read admin token: %w", err)
	}
	buf := make([]byte, 24)
	rand.Read(buf)
	tok := "lza_" + hex.EncodeToString(buf)
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return fmt.Errorf("write admin token: %w", err)
	}
	cfg.AdminToken = tok
	return nil
}
