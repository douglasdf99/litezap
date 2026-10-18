package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCloudDefaultsAreOff(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OperatorToken != "" || len(cfg.OperatorTokens()) != 0 || len(cfg.LockedSettings) != 0 || cfg.RequireProxy ||
		cfg.MaxInstances != 0 || cfg.OpsListenAddr != "" || cfg.MessageRetentionDays != 0 || cfg.MediaRetentionDays != 0 ||
		cfg.ZombieTimeout != 0 || cfg.ShutdownTimeout != 0 {
		t.Fatalf("cloud options must default to off: %+v", cfg)
	}
}

func TestConfigJSONEnvThenVariables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "litezap.yaml")
	if err := os.WriteFile(p, []byte("listen_addr: 0.0.0.0:9000\nmax_instances: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITEZAP_CONFIG_JSON", `{"max_instances": 5, "webhook_retention_days": 3,
		"instance_defaults": {"max_per_minute": 10, "timezone": "America/Sao_Paulo"},
		"locked_settings": ["proxy_url"], "zombie_timeout": "5m"}`)
	t.Setenv("LITEZAP_MAX_INSTANCES", "7")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("file value lost: %q", cfg.ListenAddr)
	}
	if cfg.MaxInstances != 7 {
		t.Errorf("max_instances = %d, want the variable to win", cfg.MaxInstances)
	}
	if cfg.WebhookRetentionDays != 3 || cfg.InstanceDefaults.MaxPerMinute != 10 ||
		cfg.InstanceDefaults.Timezone != "America/Sao_Paulo" || cfg.InstanceDefaults.MinIntervalMS != 3000 {
		t.Errorf("config json not merged: %+v", cfg)
	}
	if !slices.Equal(cfg.LockedSettings, []string{"proxy_url"}) || cfg.ZombieTimeout != 5*time.Minute {
		t.Errorf("locked = %v zombie = %v", cfg.LockedSettings, cfg.ZombieTimeout)
	}
}

func TestCloudEnvVariables(t *testing.T) {
	t.Setenv("LITEZAP_OPERATOR_TOKEN", " lzo_aaaaaaaaaaaaaaaaaaaa , lzo_bbbbbbbbbbbbbbbbbbbb ")
	t.Setenv("LITEZAP_LOCKED_SETTINGS", "proxy_url, unsafe_pacing")
	t.Setenv("LITEZAP_REQUIRE_PROXY", "true")
	t.Setenv("LITEZAP_OPS_LISTEN_ADDR", "[::]:9090")
	t.Setenv("LITEZAP_MESSAGE_RETENTION_DAYS", "90")
	t.Setenv("LITEZAP_MEDIA_RETENTION_DAYS", "7")
	t.Setenv("LITEZAP_WEBHOOK_RETENTION_DAYS", "2")
	t.Setenv("LITEZAP_WEBHOOK_FAILED_RETENTION_DAYS", "4")
	t.Setenv("LITEZAP_ZOMBIE_TIMEOUT", "4m")
	t.Setenv("LITEZAP_SHUTDOWN_TIMEOUT", "25s")
	t.Setenv("LITEZAP_INSTANCE_DEFAULTS_JSON", `{"jitter_ms": 500}`)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.OperatorTokens(); !slices.Equal(got, []string{"lzo_aaaaaaaaaaaaaaaaaaaa", "lzo_bbbbbbbbbbbbbbbbbbbb"}) {
		t.Errorf("operator tokens = %v", got)
	}
	if !slices.Equal(cfg.LockedSettings, []string{"proxy_url", "unsafe_pacing"}) || !cfg.RequireProxy ||
		cfg.OpsListenAddr != "[::]:9090" || cfg.MessageRetentionDays != 90 || cfg.MediaRetentionDays != 7 ||
		cfg.WebhookRetentionDays != 2 || cfg.WebhookFailedRetentionDays != 4 ||
		cfg.ZombieTimeout != 4*time.Minute || cfg.ShutdownTimeout != 25*time.Second || cfg.InstanceDefaults.JitterMS != 500 {
		t.Errorf("unexpected: %+v", cfg)
	}
}

func TestCloudValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"short operator":    {"LITEZAP_OPERATOR_TOKEN": "short"},
		"three operators":   {"LITEZAP_OPERATOR_TOKEN": "lzo_aaaaaaaaaaaaaaaaaaaa,lzo_bbbbbbbbbbbbbbbbbbbb,lzo_cccccccccccccccccccc"},
		"operator is admin": {"LITEZAP_OPERATOR_TOKEN": "lza_sameeeeeeeeeeeeeeeeee", "LITEZAP_ADMIN_TOKEN": "lza_sameeeeeeeeeeeeeeeeee"},
		"unknown lock":      {"LITEZAP_LOCKED_SETTINGS": "proxy_url,timezone"},
		"negative max":      {"LITEZAP_MAX_INSTANCES": "-1"},
		"bad bool":          {"LITEZAP_REQUIRE_PROXY": "maybe"},
		"negative days":     {"LITEZAP_MESSAGE_RETENTION_DAYS": "-3"},
		"tiny zombie":       {"LITEZAP_ZOMBIE_TIMEOUT": "5s"},
		"tiny shutdown":     {"LITEZAP_SHUTDOWN_TIMEOUT": "1s"},
		"bad json":          {"LITEZAP_CONFIG_JSON": "{"},
		"bad defaults":      {"LITEZAP_INSTANCE_DEFAULTS_JSON": `{"max_per_minute": 1000}`},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(""); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
