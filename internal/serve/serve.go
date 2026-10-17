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
	// Bind before touching workers: a second `serve` on the same data dir must fail here,
	// not after CleanupOrphans has killed the running supervisor's workers.
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.ListenAddr, err)
	}
	defer ln.Close()
	st, err := store.Open(filepath.Join(cfg.DataDir, "litezap.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	spawner.CleanupOrphans(cfg.DataDir, log)
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	if err := mgr.Boot(ctx); err != nil {
		shutdownManager(mgr)
		return err
	}

	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir}
	handler := api.New(svc, api.Options{
		AdminToken: cfg.AdminToken,
		ProtectOps: !isLoopback(cfg.ListenAddr),
		Ready:      st.Ping,
	}, log)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: Delete/Logout can take ~20s (worker stop), and SSE streams
		// (later milestones) stay open indefinitely.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("litezap listening", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir,
			"admin_token_file", filepath.Join(cfg.DataDir, "admin_token"))
		errc <- srv.Serve(ln)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(hctx)
	cancel()
	shutdownManager(mgr)
	return serveErr
}

// shutdownManager halts every worker with its own budget, so supervisor cleanups finish
// their store writes before the deferred st.Close runs.
func shutdownManager(mgr *manager.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), manager.DefaultOptions().StopTimeout+5*time.Second)
	defer cancel()
	mgr.Shutdown(ctx)
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
