// Package serve wires the supervisor process (`litezap serve`).
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/dashboard"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
)

// Options configure RunWith beyond the config file.
type Options struct {
	ConfigPath string
	// Base is the configuration the file and LITEZAP_* variables apply on top of
	// (zero value: config.Default()).
	Base *config.Config
	// LaunchCodes enables one-click dashboard login with codes minted by the caller (the
	// tray app). Ignored unless the server listens on loopback.
	LaunchCodes *dashboard.LaunchCodes
	// OnListening is called once the HTTP server accepts connections.
	OnListening func(Info)
}

// Info describes a running server.
type Info struct {
	// URL is the base URL of the API (http://<listen_addr>); the dashboard is URL+"/app/".
	URL        string
	DataDir    string
	AdminToken string
	// Dashboard reports whether /app/ is served; OneClickLogin whether launch codes from
	// Options.LaunchCodes are accepted (dashboard on and a loopback listen address).
	Dashboard     bool
	OneClickLogin bool
}

func Run(ctx context.Context, cfgPath string, log *slog.Logger) error {
	return RunWith(ctx, Options{ConfigPath: cfgPath}, log)
}

func RunWith(ctx context.Context, opt Options, log *slog.Logger) error {
	base := config.Default()
	if opt.Base != nil {
		base = *opt.Base
	}
	cfg, err := config.LoadWith(opt.ConfigPath, base)
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
	// Events: webhooks are the bus's persistent sink; SSE streams subscribe per request.
	day := 24 * time.Hour
	hooks := webhook.New(st, webhook.Options{AllowPrivate: cfg.WebhookAllowPrivateURLs, Log: log,
		Retention:       time.Duration(cfg.WebhookRetentionDays) * day,
		FailedRetention: time.Duration(cfg.WebhookFailedRetentionDays) * day})
	bus := events.NewBus(nil, hooks)
	bus.SetLogger(log)
	history := events.NewHistoryTracker(bus, nil, 15*time.Second)
	q := queue.New(st, mgr, queue.Options{Defaults: cfg.InstanceDefaults, Log: log, Resolver: resolver,
		ConnectGrace: 15 * time.Second, Events: bus})
	mgr.SetObserver(manager.Observers{
		&ingest.Ingestor{Store: st, Log: log, Events: bus, History: history}, q, events.Observer{Bus: bus},
	})
	stopAll := func() { shutdownAll(q, mgr, history, hooks) }
	// Before any worker starts: sends interrupted by the last shutdown go back to the queue.
	if n, err := q.Recover(ctx); err != nil {
		stopAll()
		return fmt.Errorf("recover send queue: %w", err)
	} else if n > 0 {
		log.Warn("requeued sends interrupted by the last shutdown", "count", n)
	}
	if err := hooks.Start(ctx); err != nil {
		stopAll()
		return fmt.Errorf("start webhooks: %w", err)
	}
	if err := mgr.Boot(ctx); err != nil {
		stopAll()
		return err
	}

	baseURL := cfg.PublicURL
	if baseURL == "" {
		baseURL = "http://" + cfg.ListenAddr
	}
	mediaKey, err := media.LoadOrCreateKey(filepath.Join(cfg.DataDir, "media_key"))
	if err != nil {
		stopAll()
		return fmt.Errorf("media key: %w", err)
	}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir, Queues: q, Events: bus}
	msg := &service.Messaging{
		Store: st, Manager: mgr, Queues: q, Resolver: resolver,
		Fetcher:  &media.Fetcher{AllowPrivate: cfg.MediaAllowPrivateURLs},
		Signer:   media.NewSigner(mediaKey, baseURL),
		DataDir:  cfg.DataDir,
		Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL,
		// A URL built from a non-loopback listen address may be unreachable for third parties.
		SignedURLs: cfg.PublicURL != "" || isLoopback(cfg.ListenAddr),
	}
	loopback := isLoopback(cfg.ListenAddr)
	handler := api.New(svc, msg, api.Options{
		// MCP streamable HTTP at /mcp, behind the same tokens (spec 3.1, 6.1).
		MCP: mcp.NewHTTPHandler(mcp.HTTPDeps{Instances: svc, Messaging: msg, Events: bus, Logger: log,
			PublicHost: publicHost(cfg.PublicURL)}),
		AdminToken: cfg.AdminToken,
		ProtectOps: !loopback,
		Ready:      st.Ping,
		Webhooks:   &service.Webhooks{Store: st, Dispatcher: hooks},
		Events:     bus,
	}, log)
	var codes *dashboard.LaunchCodes
	if loopback {
		codes = opt.LaunchCodes
	}
	srv := &http.Server{
		Handler:           withDashboard(handler, !cfg.DisableDashboard, cfg.AdminToken, codes),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: Delete/Logout can take ~20s (worker stop), and SSE streams stay
		// open indefinitely (each SSE write sets its own deadline).
	}
	// Shutdown waits for active requests: end the SSE streams so it does not wait for them.
	srv.RegisterOnShutdown(bus.CloseSubscribers)

	errc := make(chan error, 1)
	go func() {
		log.Info("litezap listening", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir,
			"admin_token_file", filepath.Join(cfg.DataDir, "admin_token"))
		if !cfg.DisableDashboard {
			log.Info("dashboard", "url", dashboardURL(ln.Addr().String())+dashboard.Prefix)
		}
		errc <- srv.Serve(ln)
	}()
	if opt.OnListening != nil {
		opt.OnListening(Info{URL: dashboardURL(ln.Addr().String()), DataDir: cfg.DataDir,
			AdminToken: cfg.AdminToken, Dashboard: !cfg.DisableDashboard,
			OneClickLogin: !cfg.DisableDashboard && codes != nil})
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(hctx); err != nil {
		// Requests still running after the grace period are cut, so none outlives the
		// queues, workers and store stopped below.
		_ = srv.Close()
	}
	cancel()
	stopAll()
	return serveErr
}

// shutdownAll stops the dispatchers (a send in flight gets up to 10s, spec 9.2), then
// halts every worker with its own budget, so supervisor cleanups finish their store
// writes before the deferred st.Close runs. Events produced until then still reach the
// webhook outbox: the history tracker flushes, then the webhook dispatcher writes its
// buffer and gives deliveries in flight up to 5s (cut ones are resent after the restart).
func shutdownAll(q *queue.Queues, mgr *manager.Manager, history *events.HistoryTracker, hooks *webhook.Dispatcher) {
	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	q.Shutdown(qctx)
	qcancel()
	ctx, cancel := context.WithTimeout(context.Background(), manager.DefaultOptions().StopTimeout+5*time.Second)
	mgr.Shutdown(ctx)
	cancel()
	history.Close()
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	hooks.Close(hctx)
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

// publicHost is the host[:port] of public_url ("" when unset or invalid).
func publicHost(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// withDashboard mounts the embedded dashboard at /app/ (and redirects / there) in front
// of the API router.
func withDashboard(apiHandler http.Handler, enabled bool, adminToken string, codes *dashboard.LaunchCodes) http.Handler {
	if !enabled {
		return apiHandler
	}
	mux := http.NewServeMux()
	mux.Handle(dashboard.Prefix, dashboard.Handler(dashboard.Options{AdminToken: adminToken, LaunchCodes: codes}))
	mux.Handle("GET /{$}", http.RedirectHandler(dashboard.Prefix, http.StatusFound))
	mux.Handle("/", apiHandler)
	return mux
}

// dashboardURL turns the bound address into a browsable base URL (an unspecified host
// such as 0.0.0.0 becomes 127.0.0.1).
func dashboardURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
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
