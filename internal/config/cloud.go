package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/secure"
	"gopkg.in/yaml.v3"
)

// Settings that locked_settings may name: only an operator token changes them.
var LockableSettings = []string{"proxy_url", "unsafe_pacing"}

// minOperatorToken is the shortest accepted operator token (it is above the admin).
const minOperatorToken = 20

// OperatorTokens returns the configured operator tokens (operator_token holds up to two,
// comma-separated, so a new one can be rolled out before the old one is dropped).
func (c Config) OperatorTokens() []string {
	var out []string
	for _, t := range strings.Split(c.OperatorToken, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// applyConfigJSON merges LITEZAP_CONFIG_JSON (the whole configuration document as JSON,
// for platforms configured only by environment) over the file. Individual LITEZAP_*
// variables are applied afterwards and win.
func applyConfigJSON(cfg *Config) error {
	v := strings.TrimSpace(os.Getenv("LITEZAP_CONFIG_JSON"))
	if v == "" {
		return nil
	}
	if err := yaml.Unmarshal([]byte(v), cfg); err != nil { // JSON is valid YAML
		return fmt.Errorf("LITEZAP_CONFIG_JSON: %w", err)
	}
	return nil
}

// applyCloudEnv reads the LITEZAP_* variables of the options added for managed hosting
// (and of the options that used to be file-only).
func applyCloudEnv(cfg *Config) error {
	if v := strings.TrimSpace(os.Getenv("LITEZAP_INSTANCE_DEFAULTS_JSON")); v != "" {
		if err := yaml.Unmarshal([]byte(v), &cfg.InstanceDefaults); err != nil {
			return fmt.Errorf("LITEZAP_INSTANCE_DEFAULTS_JSON: %w", err)
		}
	}
	if v := os.Getenv("LITEZAP_OPERATOR_TOKEN"); strings.TrimSpace(v) != "" {
		cfg.OperatorToken = v
	}
	if v, ok := os.LookupEnv("LITEZAP_LOCKED_SETTINGS"); ok {
		cfg.LockedSettings = nil
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.LockedSettings = append(cfg.LockedSettings, s)
			}
		}
	}
	if v := os.Getenv("LITEZAP_OPS_LISTEN_ADDR"); v != "" {
		cfg.OpsListenAddr = v
	}
	if err := envBool("LITEZAP_REQUIRE_PROXY", &cfg.RequireProxy); err != nil {
		return err
	}
	for name, dst := range map[string]*int{
		"LITEZAP_MAX_INSTANCES":                 &cfg.MaxInstances,
		"LITEZAP_MESSAGE_RETENTION_DAYS":        &cfg.MessageRetentionDays,
		"LITEZAP_MEDIA_RETENTION_DAYS":          &cfg.MediaRetentionDays,
		"LITEZAP_WEBHOOK_RETENTION_DAYS":        &cfg.WebhookRetentionDays,
		"LITEZAP_WEBHOOK_FAILED_RETENTION_DAYS": &cfg.WebhookFailedRetentionDays,
	} {
		if err := envInt(name, dst); err != nil {
			return err
		}
	}
	for name, dst := range map[string]*time.Duration{
		"LITEZAP_ZOMBIE_TIMEOUT":   &cfg.ZombieTimeout,
		"LITEZAP_SHUTDOWN_TIMEOUT": &cfg.ShutdownTimeout,
	} {
		if v := os.Getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			*dst = d
		}
	}
	return nil
}

func envBool(name string, dst *bool) error {
	if v := os.Getenv(name); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = b
	}
	return nil
}

func envInt(name string, dst *int) error {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = n
	}
	return nil
}

func validateCloud(cfg Config) error {
	ops := cfg.OperatorTokens()
	if len(ops) > 2 {
		return errors.New("operator_token takes at most two comma-separated tokens (current and next)")
	}
	for _, t := range ops {
		if len(t) < minOperatorToken {
			return fmt.Errorf("operator_token must have at least %d characters", minOperatorToken)
		}
		if cfg.AdminToken != "" && secure.Equal(t, cfg.AdminToken) {
			return errors.New("operator_token must differ from admin_token")
		}
	}
	for _, s := range cfg.LockedSettings {
		ok := false
		for _, l := range LockableSettings {
			ok = ok || s == l
		}
		if !ok {
			return fmt.Errorf("locked_settings: %q cannot be locked (allowed: %s)", s, strings.Join(LockableSettings, ", "))
		}
	}
	switch {
	case cfg.MaxInstances < 0:
		return errors.New("max_instances must be 0 (no limit) or more")
	case cfg.MessageRetentionDays < 0 || cfg.MediaRetentionDays < 0:
		return errors.New("message_retention_days and media_retention_days must be 0 (keep forever) or more")
	case cfg.ZombieTimeout != 0 && cfg.ZombieTimeout < 30*time.Second:
		return errors.New("zombie_timeout must be 0 (off) or at least 30s")
	case cfg.ShutdownTimeout != 0 && cfg.ShutdownTimeout < 5*time.Second:
		return errors.New("shutdown_timeout must be 0 (default phases) or at least 5s")
	}
	return nil
}
