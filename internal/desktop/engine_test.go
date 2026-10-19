package desktop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalEngine(t *testing.T) {
	for _, k := range []string{"LITEZAP_LISTEN_ADDR", "LITEZAP_ADMIN_TOKEN", "LITEZAP_DATA_DIR", "LITEZAP_CONFIG_JSON"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	if _, err := LocalEngine(dir); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no token: err = %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "data"), 0o700)
	os.WriteFile(filepath.Join(dir, "data", "admin_token"), []byte("lza_test\n"), 0o600)
	e, err := LocalEngine(dir)
	if err != nil || e.URL != "http://127.0.0.1:8787" || e.Token != "lza_test" {
		t.Fatalf("engine = %+v, %v", e, err)
	}
	os.WriteFile(ConfigPath(dir), []byte("listen_addr: 0.0.0.0:9999\n"), 0o600)
	if e, _ := LocalEngine(dir); e.URL != "http://127.0.0.1:9999" {
		t.Errorf("URL with litezap.yaml = %q", e.URL)
	}
}

func TestEngineRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if !(Engine{URL: srv.URL}).Running(context.Background(), time.Second) {
		t.Error("running server reported down")
	}
	srv.Close()
	if (Engine{URL: srv.URL}).Running(context.Background(), time.Second) {
		t.Error("closed server reported running")
	}
}
