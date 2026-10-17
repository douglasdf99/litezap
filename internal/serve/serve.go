// Package serve wires the supervisor process (`litezap serve`).
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/store"
)

func Run(ctx context.Context, cfgPath string, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if err := config.EnsureAdminToken(&cfg); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "litezap.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	spawner.CleanupOrphans(cfg.DataDir, log)
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	if err := mgr.Boot(ctx); err != nil {
		return err
	}

	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir}
	handler := api.New(svc, api.Options{
		AdminToken: cfg.AdminToken,
		ProtectOps: !isLoopback(cfg.ListenAddr),
		Ready:      st.Ping,
	}, log)
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() {
		log.Info("litezap listening", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir,
			"admin_token_file", filepath.Join(cfg.DataDir, "admin_token"))
		errc <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	mgr.Shutdown(sctx)
	return serveErr
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
