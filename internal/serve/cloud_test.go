package serve

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// require_proxy is also enforced where the worker is spawned: never a direct connection.
func TestWorkerEnvRefusesDirectWhenProxyRequired(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateInstance(ctx, store.Instance{ID: "i", Name: "i", State: instance.Created, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RequireProxy = true
	if _, err := workerEnv(st, cfg)(ctx, "i"); !errors.Is(err, errProxyRequired) {
		t.Fatalf("env without proxy = %v", err)
	}
	cfg.DefaultProxyURL = "socks5://p:1080"
	if got, err := workerEnv(st, cfg)(ctx, "i"); err != nil || got[0] != "LITEZAP_WORKER_PROXY_URL=socks5://p:1080" {
		t.Fatalf("env with default proxy = %v, %v", got, err)
	}
}
