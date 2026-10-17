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
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
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
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log,
		Env: workerEnv(st, cfg)}
	mgr := manager.New(st, sp, manager.DefaultOptions(), log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	q := queue.New(st, mgr, queue.Options{Defaults: cfg.InstanceDefaults, Log: log, Resolver: resolver,
		ConnectGrace: 15 * time.Second})
	mgr.SetObserver(manager.Observers{&ingest.Ingestor{Store: st, Log: log}, q})
	// Before any worker starts: sends interrupted by the last shutdown go back to the queue.
	if n, err := q.Recover(ctx); err != nil {
		return fmt.Errorf("recover send queue: %w", err)
	} else if n > 0 {
		log.Warn("requeued sends interrupted by the last shutdown", "count", n)
	}
	if err := mgr.Boot(ctx); err != nil {
		shutdownAll(q, mgr)
		return err
	}

	baseURL := cfg.PublicURL
	if baseURL == "" {
		baseURL = "http://" + cfg.ListenAddr
	}
	mediaKey, err := media.LoadOrCreateKey(filepath.Join(cfg.DataDir, "media_key"))
	if err != nil {
		shutdownAll(q, mgr)
		return fmt.Errorf("media key: %w", err)
	}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir, Queues: q}
	msg := &service.Messaging{
		Store: st, Manager: mgr, Queues: q, Resolver: resolver,
		Fetcher:  &media.Fetcher{AllowPrivate: cfg.MediaAllowPrivateURLs},
		Signer:   media.NewSigner(mediaKey, baseURL),
		DataDir:  cfg.DataDir,
		Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL,
		// A URL built from a non-loopback listen address may be unreachable for third parties.
		SignedURLs: cfg.PublicURL != "" || isLoopback(cfg.ListenAddr),
	}
	handler := api.New(svc, msg, api.Options{
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
	shutdownAll(q, mgr)
	return serveErr
}

// shutdownAll stops the dispatchers (a send in flight gets up to 10s, spec 9.2), then
// halts every worker with its own budget, so supervisor cleanups finish their store
// writes before the deferred st.Close runs.
func shutdownAll(q *queue.Queues, mgr *manager.Manager) {
	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	q.Shutdown(qctx)
	qcancel()
	ctx, cancel := context.WithTimeout(context.Background(), manager.DefaultOptions().StopTimeout+5*time.Second)
	defer cancel()
	mgr.Shutdown(ctx)
}

// workerEnv passes each worker its effective proxy (instance setting, else the default);
// an empty value means a direct connection. The spawner strips HTTP(S)_PROXY and friends.
func workerEnv(st *store.Store, cfg config.Config) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, id string) ([]string, error) {
		in, err := st.GetInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		set, err := settings.Effective(cfg.InstanceDefaults, cfg.DefaultProxyURL, in.SettingsJSON)
		if err != nil {
			return nil, err
		}
		return []string{"LITEZAP_WORKER_PROXY_URL=" + set.ProxyURL}, nil
	}
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
