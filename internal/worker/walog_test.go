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
	l := newSlogWA(slog.New(slog.NewJSONHandler(&b, nil)), slog.LevelWarn).Sub("Client")
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
