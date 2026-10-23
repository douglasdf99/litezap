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
	"time"

	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/settings"
	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddr     string `yaml:"listen_addr"`
	PublicURL      string `yaml:"public_url"`
	DataDir        string `yaml:"data_dir"`
	AdminToken     string `yaml:"admin_token"`
	WorkerMemoryMB int64  `yaml:"worker_memory_max_mb"`
	// DefaultProxyURL is used by every instance without its own settings.proxy_url.
	DefaultProxyURL string `yaml:"default_proxy_url"`
	// MediaAllowPrivateURLs disables the SSRF block on media.url downloads (local use).
	MediaAllowPrivateURLs bool `yaml:"media_allow_private_urls"`
	// WebhookAllowPrivateURLs lets webhooks reach private and loopback addresses (local use).
	WebhookAllowPrivateURLs bool `yaml:"webhook_allow_private_urls"`
	// WebhookRetentionDays keeps delivered webhook deliveries; WebhookFailedRetentionDays
	// keeps the dead-letter.
	WebhookRetentionDays       int `yaml:"webhook_retention_days"`
	WebhookFailedRetentionDays int `yaml:"webhook_failed_retention_days"`
	// InstanceDefaults are the pacing and ramp defaults (spec 4.1); instances override them.
	InstanceDefaults settings.Settings `yaml:"instance_defaults"`
	// DisableDashboard turns off the embedded web dashboard at /app/ (the API is unaffected).
	DisableDashboard bool `yaml:"disable_dashboard"`

	// Managed hosting (all off by default; see cloud.go and the README).
	// OperatorToken (one or two comma-separated values) is above the admin token: only it
	// changes LockedSettings, bypasses MaxInstances and calls /v1/operator/*.
	OperatorToken  string   `yaml:"operator_token"`
	LockedSettings []string `yaml:"locked_settings"`
	// RequireProxy refuses to start a worker without a proxy (never a direct connection).
	RequireProxy bool `yaml:"require_proxy"`
	// MaxInstances caps instances created with the admin token (0: no limit).
	MaxInstances int `yaml:"max_instances"`
	// OpsListenAddr serves /healthz, /readyz and /metrics without a token ("" = off).
	OpsListenAddr string `yaml:"ops_listen_addr"`
	// MessageRetentionDays and MediaRetentionDays prune old messages and local media
	// copies (0: keep forever).
	MessageRetentionDays int `yaml:"message_retention_days"`
	MediaRetentionDays   int `yaml:"media_retention_days"`
	// ZombieTimeout forces a reconnect of a worker stuck "connected" without a working
	// socket (0: off).
	ZombieTimeout time.Duration `yaml:"zombie_timeout"`
	// ShutdownTimeout bounds the whole graceful shutdown (0: the default phase timeouts).
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// WALogLevel is the level of whatsmeow's logs in the workers (debug | info | warn |
	// error; "": warn). At debug the lines keep JIDs (phone numbers): troubleshooting only.
	WALogLevel string `yaml:"wa_log_level"`

	// Platform and partners (marco 11): IP allowlists and the hosted pairing page.
	Platform `yaml:",inline"`

	// WhatsAppCatalog is the public catalog GraphQL transport (marco 12): the app token
	// and the persisted query ids of WA Web, which Meta rotates. Empty values take the
	// defaults of the 2026-10-21 snapshot; update them here, without a release, when the
	// API answers upstream_changed.
	WhatsAppCatalog catalog.Config `yaml:"whatsapp_catalog"`
}

func Default() Config {
	return Config{
		ListenAddr: "127.0.0.1:8787", DataDir: "data", WorkerMemoryMB: 512,
		WebhookRetentionDays: 7, WebhookFailedRetentionDays: 30,
		InstanceDefaults: settings.Defaults(),
	}
}

// Load reads path (a missing file means defaults) and applies env overrides.
// DataDir is always returned as an absolute path.
func Load(path string) (Config, error) { return LoadWith(path, Default()) }

// LoadWith is Load starting from base instead of Default (the tray app uses it to point
// data_dir at the per-user app folder while the file and env still win).
func LoadWith(path string, base Config) (Config, error) {
	cfg := base
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
	if err := applyConfigJSON(&cfg); err != nil {
		return cfg, err
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
	if v := strings.TrimSpace(os.Getenv("LITEZAP_ADMIN_TOKEN")); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("LITEZAP_WORKER_MEMORY_MAX_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_WORKER_MEMORY_MAX_MB: %w", err)
		}
		cfg.WorkerMemoryMB = n
	}
	if v := os.Getenv("LITEZAP_DEFAULT_PROXY_URL"); v != "" {
		cfg.DefaultProxyURL = v
	}
	if v := os.Getenv("LITEZAP_MEDIA_ALLOW_PRIVATE_URLS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_MEDIA_ALLOW_PRIVATE_URLS: %w", err)
		}
		cfg.MediaAllowPrivateURLs = b
	}
	if v := os.Getenv("LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS: %w", err)
		}
		cfg.WebhookAllowPrivateURLs = b
	}
	if v := os.Getenv("LITEZAP_DISABLE_DASHBOARD"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("LITEZAP_DISABLE_DASHBOARD: %w", err)
		}
		cfg.DisableDashboard = b
	}
	if v := os.Getenv("LITEZAP_WA_LOG_LEVEL"); v != "" {
		cfg.WALogLevel = v
	}
	cfg.WALogLevel = strings.ToLower(strings.TrimSpace(cfg.WALogLevel))
	switch cfg.WALogLevel {
	case "", "debug", "info", "warn", "error":
	default:
		return cfg, fmt.Errorf("wa_log_level must be debug, info, warn or error, not %q", cfg.WALogLevel)
	}
	if err := applyCloudEnv(&cfg); err != nil {
		return cfg, err
	}
	if err := validateCloud(cfg); err != nil {
		return cfg, err
	}
	applyPlatformEnv(&cfg)
	if err := validatePlatform(&cfg); err != nil {
		return cfg, err
	}
	if cfg.WebhookRetentionDays < 1 || cfg.WebhookFailedRetentionDays < 1 {
		return cfg, errors.New("webhook_retention_days and webhook_failed_retention_days must be at least 1")
	}
	if err := settings.ValidateProxyURL(cfg.DefaultProxyURL); err != nil {
		return cfg, fmt.Errorf("default_proxy_url: %w", err)
	}
	if err := cfg.InstanceDefaults.Validate(); err != nil {
		return cfg, fmt.Errorf("instance_defaults: %w", err)
	}
	if err := cfg.WhatsAppCatalog.Validate(); err != nil {
		return cfg, err
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
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return fmt.Errorf("admin token file %s is empty", p)
		}
		cfg.AdminToken = tok
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
