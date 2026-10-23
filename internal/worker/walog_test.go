package worker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogWARedactsAndFilters(t *testing.T) {
	var b bytes.Buffer
	l := newSlogWA(slog.New(slog.NewJSONHandler(&b, nil)), slog.LevelWarn, true).Sub("Client")
	l.Infof("noise")
	l.Warnf("failed to decrypt message from %s", "5511988887777@s.whatsapp.net")
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %q", b.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if msg := m["msg"].(string); strings.Contains(msg, "5511988887777") || !strings.Contains(msg, "[redacted]") || m["submodule"] != "Client" {
		t.Fatalf("entry = %v", m)
	}
}

func TestSlogWAKeepsJIDsAtDebug(t *testing.T) {
	var b bytes.Buffer
	l := newSlogWA(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), slog.LevelDebug, false)
	l.Debugf("node from %s", "5511900000000@s.whatsapp.net")
	if !strings.Contains(b.String(), "5511900000000") || !strings.Contains(b.String(), "module=whatsmeow") {
		t.Fatalf("debug line = %q", b.String())
	}
}

func TestLogLevels(t *testing.T) {
	t.Setenv("LITEZAP_LOG_LEVEL", "")
	t.Setenv("LITEZAP_WA_LOG_LEVEL", "")
	if LogLevel() != slog.LevelInfo || WALogLevel() != slog.LevelWarn {
		t.Fatalf("defaults = %v %v", LogLevel(), WALogLevel())
	}
	t.Setenv("LITEZAP_LOG_LEVEL", "Debug")
	t.Setenv("LITEZAP_WA_LOG_LEVEL", "error")
	if LogLevel() != slog.LevelDebug || WALogLevel() != slog.LevelError {
		t.Fatalf("env = %v %v", LogLevel(), WALogLevel())
	}
	t.Setenv("LITEZAP_WA_LOG_LEVEL", "nonsense")
	if WALogLevel() != slog.LevelWarn {
		t.Fatalf("invalid = %v", WALogLevel())
	}
}
