package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewLogger(t *testing.T) {
	var b bytes.Buffer
	newLogger(&b, "json", false).Info("hello", "instance_id", "inst_1")
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil || m["msg"] != "hello" || m["instance_id"] != "inst_1" {
		t.Fatalf("json log = %q (%v)", b.String(), err)
	}
	b.Reset()
	newLogger(&b, "", true).Info("hello")
	if !strings.Contains(b.String(), "msg=hello") {
		t.Fatalf("text log = %q", b.String())
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("X_D", "5m")
	if envDuration("X_D") != 5*time.Minute {
		t.Fatal("5m")
	}
	t.Setenv("X_D", "nope")
	if envDuration("X_D") != 0 {
		t.Fatal("invalid must be 0")
	}
}
