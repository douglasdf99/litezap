package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8787" || cfg.WorkerMemoryMB != 512 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("data dir must be absolute, got %q", cfg.DataDir)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.yaml")
	if err := os.WriteFile(p, []byte("listen_addr: 0.0.0.0:9000\nadmin_token: fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITEZAP_ADMIN_TOKEN", "fromenv")
	t.Setenv("LITEZAP_WORKER_MEMORY_MAX_MB", "256")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("listen addr = %q", cfg.ListenAddr)
	}
	if cfg.AdminToken != "fromenv" {
		t.Errorf("admin token = %q", cfg.AdminToken)
	}
	if cfg.WorkerMemoryMB != 256 {
		t.Errorf("memory = %d", cfg.WorkerMemoryMB)
	}
}

func TestLoadRejectsBadMemoryEnv(t *testing.T) {
	t.Setenv("LITEZAP_WORKER_MEMORY_MAX_MB", "lots")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureAdminTokenCreatesAndReuses(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	if err := EnsureAdminToken(&cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.AdminToken, "lza_") {
		t.Fatalf("token = %q", cfg.AdminToken)
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, "admin_token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", info.Mode().Perm())
	}
	again := Config{DataDir: cfg.DataDir}
	if err := EnsureAdminToken(&again); err != nil {
		t.Fatal(err)
	}
	if again.AdminToken != cfg.AdminToken {
		t.Errorf("token not reused: %q vs %q", again.AdminToken, cfg.AdminToken)
	}
}

func TestEnsureAdminTokenKeepsConfigured(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminToken: "set"}
	if err := EnsureAdminToken(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AdminToken != "set" {
		t.Fatal("configured token overwritten")
	}
}

func TestEnsureAdminTokenRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "admin_token"), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: dir}
	err := EnsureAdminToken(&cfg)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("expected empty-file error, got %v", err)
	}
}

func TestLoadTreatsWhitespaceAdminTokenEnvAsUnset(t *testing.T) {
	t.Setenv("LITEZAP_ADMIN_TOKEN", "   ")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminToken != "" {
		t.Fatalf("token = %q", cfg.AdminToken)
	}
}
