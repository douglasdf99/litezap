// Package serve wires the supervisor process (`litezap serve`).
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/agents"
	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/autoread"
	"github.com/douglasdf99/litezap/internal/catalog"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/dashboard"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/ingest"
	"github.com/douglasdf99/litezap/internal/localai"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/manager"
	"github.com/douglasdf99/litezap/internal/mcp"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/pairing"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/realtime"
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
	// AIClients enables the dashboard's "Conectar a IA" page (the tray app). Ignored
	// unless the server listens on loopback.
	AIClients dashboard.AIConnector
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

	cl := newCloud(cfg, st, log)
	if err := cl.listenOps(); err != nil {
		return err
	}

	spawner.CleanupOrphans(cfg.DataDir, log)
	sp := &spawner.Process{DataDir: cfg.DataDir, MemoryLimitMB: cfg.WorkerMemoryMB, Log: log,
		Env: workerEnv(st, cfg)}
	// shutdown_timeout shrinks every shutdown phase proportionally (E12).
	scale := shutdownScale(cfg.ShutdownTimeout)
	mopt := manager.DefaultOptions()
	mopt.StopTimeout = scaled(mopt.StopTimeout, scale)
	mgr := manager.New(st, sp, mopt, log)
	resolver := &lookup.Resolver{Store: st, Workers: mgr}
	// Events: webhooks are the bus's persistent sink; SSE streams subscribe per request.
	day := 24 * time.Hour
	hooks := webhook.New(st, webhook.Options{AllowPrivate: cfg.WebhookAllowPrivateURLs, Log: log,
		Retention:       time.Duration(cfg.WebhookRetentionDays) * day,
		FailedRetention: time.Duration(cfg.WebhookFailedRetentionDays) * day})
	// Built-in attendant (spec 4.7): the agent hook's "builtin" target. Provider keys are
	// encrypted with data/attendant_key.
	vault, err := attendant.LoadVault(filepath.Join(cfg.DataDir, "attendant_key"))
	if err != nil {
		return fmt.Errorf("attendant key: %w", err)
	}
	att := &attendant.Attendant{Store: st, Vault: vault, Log: log,
		Location: func(ctx context.Context, id string) *time.Location {
			in, err := st.GetInstance(ctx, id)
			if err != nil {
				return nil
			}
			set, err := settings.Effective(cfg.InstanceDefaults, cfg.DefaultProxyURL, in.SettingsJSON)
			if err != nil {
				return nil
			}
			return set.Location()
		}}
	// LiteZap agents (spec 2026-10-20): the hook's "agents" target, a tool-use loop over
	// the MCP tool table (set below, once the services exist) with the attendant's provider.
	agentsRunner := &agents.Runner{Store: st, Sessions: agents.FromAttendant(att), Log: log,
		Codex: agents.NewCodexCLI(log), Location: att.Location}
	// Agent hook (spec 4.3): another bus sink; its send path is set once Messaging exists.
	agent := agenthook.New(st, agenthook.Options{AllowPrivate: cfg.WebhookAllowPrivateURLs, Log: log,
		Held: cl.sending.Held, Builtin: att, Agents: agentsRunner})
	bus := events.NewBus(nil, hooks, cl.usage, agent)
	bus.SetLogger(log)
	cl.attach(bus)
	history := events.NewHistoryTracker(bus, nil, 15*time.Second)
	q := queue.New(st, mgr, queue.Options{Defaults: cfg.InstanceDefaults, Log: log, Resolver: resolver,
		ConnectGrace: 15 * time.Second, Events: bus, Hold: cl.sending.Held, SendLatency: cl.latency})
	cl.sending.Wake = q.Wake
	// Calls and presence (marco 7); its sender is set below, once messaging exists.
	rt := &realtime.Handler{Workers: mgr, Events: bus, Log: log, FollowUp: agent.CallFollowUp,
		Settings: func(ctx context.Context, id string) (settings.Settings, error) {
			in, err := st.GetInstance(ctx, id)
			if err != nil {
				return settings.Settings{}, err
			}
			return settings.Effective(cfg.InstanceDefaults, cfg.DefaultProxyURL, in.SettingsJSON)
		}}
	// Groups (marco 8): group.updated and group chat names.
	groupEvents := &service.GroupEvents{Store: st, Workers: mgr, Events: bus, Log: log}
	// Marco 9: chat state from the app state, and auto-read; their services are set below.
	acct := &service.Account{Store: st, Events: bus, Sending: cl.sending}
	reader := &autoread.Reader{Workers: mgr, Unread: st, Log: log, Settings: rt.Settings}
	// Marco 10: status posts, channel posts and label changes.
	socialEvents := &service.SocialEvents{Store: st, Events: bus, Log: log}
	// Marco 11: instance.paired, which also spends the hosted pairing links.
	paired := &pairing.Observer{Store: st, Events: bus, Log: log}
	mgr.SetObserver(manager.Observers{
		&ingest.Ingestor{Store: st, Log: log, Events: bus, History: history}, q, events.Observer{Bus: bus}, cl.proxy, rt,
		groupEvents, acct, reader, paired, socialEvents,
	})
	stopAll := func() {
		actx, acancel := context.WithTimeout(context.Background(), scaled(5*time.Second, scale))
		agent.Close(actx) // first: it feeds the send queue
		acancel()
		shutdownAll(q, mgr, history, hooks, scale)
		rt.Close() // reject messages being queued, presence retries
		groupEvents.Close()
		reader.Close()
		paired.Close()
		cctx, cancel := context.WithTimeout(context.Background(), scaled(5*time.Second, scale))
		cl.close(cctx)
		cancel()
	}
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
	if err := cl.beforeBoot(ctx); err != nil {
		stopAll()
		return err
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
	pairingKey, err := media.LoadOrCreateKey(filepath.Join(cfg.DataDir, "pairing_key"))
	if err != nil {
		stopAll()
		return fmt.Errorf("pairing key: %w", err)
	}
	svc := &service.Instances{Store: st, Manager: mgr, DataDir: cfg.DataDir, Queues: q, Events: bus, Policy: cl.policy}
	links := &pairing.Links{Store: st, Key: pairingKey, BaseURL: baseURL}
	trusted, adminIPs, operatorIPs := cfg.IPLists()
	msg := &service.Messaging{
		Store: st, Manager: mgr, Queues: q, Resolver: resolver,
		Fetcher:  &media.Fetcher{AllowPrivate: cfg.MediaAllowPrivateURLs},
		Signer:   media.NewSigner(mediaKey, baseURL),
		DataDir:  cfg.DataDir,
		Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL,
		MediaRetention: cl.mediaRetention(),
		// A URL built from a non-loopback listen address may be unreachable for third parties.
		SignedURLs: cfg.PublicURL != "" || isLoopback(cfg.ListenAddr),
	}
	acct.Messaging = msg
	// Marco 12: WhatsApp catalog reads, through each instance's proxy (catalog.go).
	msg.Catalog = &service.CatalogReader{Client: catalog.New(cfg.WhatsAppCatalog), Store: st,
		Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL, RequireProxy: cfg.RequireProxy}
	att.Catalog = catalogKnowledge(msg.Catalog)
	loopback := isLoopback(cfg.ListenAddr)
	// New sends (REST and MCP) are refused while the operator suspends sending.
	guarded := service.GuardedMessaging{Messaging: msg, Sending: cl.sending}
	// Agent replies take the same guarded path as REST sends (queue, pacing, suspension).
	agent.SetMessenger(guarded)
	att.Media = msg // attachments: transcription and vision
	agent.SetPublisher(bus)
	rt.SetSender(guarded)
	msg.OnSettingsChanged = rt.SettingsChanged
	groups := &service.Groups{Store: st, Workers: mgr, Resolver: resolver, Sending: cl.sending,
		Fetcher: msg.Fetcher, Defaults: cfg.InstanceDefaults, DefaultProxy: cfg.DefaultProxyURL}
	social := &service.Social{Store: st, Workers: mgr, Messaging: msg, Sending: cl.sending, Fetcher: msg.Fetcher, Events: bus,
		LocationClear: cfg.BusinessLocationClear}
	attAdmin := &attendant.Admin{A: att}
	// Cardápio: LiteZap's own catalog (never written to Meta); sends take the guarded path.
	menuSvc := &service.Menu{Store: st, DataDir: cfg.DataDir, Fetcher: msg.Fetcher, Catalog: msg.Catalog, Sender: guarded,
		AI: menuAI(att)}
	// Photo files no item points at (a crash mid-upload, a deleted instance): before serving.
	if n, err := menuSvc.SweepImages(ctx); err != nil {
		log.Warn("menu: photo sweep failed", "err", err)
	} else if n > 0 {
		log.Info("menu: removed orphan photos", "count", n)
	}
	mcpDeps := mcp.HTTPDeps{Instances: svc, Messaging: guarded, Events: bus, Groups: groups, Logger: log, Account: acct,
		Social: social, Attendant: attAdmin, Menu: menuSvc, PublicHost: publicHost(cfg.PublicURL)}
	// The agents call the same tools as MCP, in process, with the instance's own scope.
	agentsRunner.Tools = func(id string) []agents.ToolDef { return agentTools(mcpDeps, id, log) }
	agentsRunner.Events = bus
	agent.Start() // after the agents have their tools
	// Drafts approved right before a crash: finish them (sends are idempotent).
	go agentsRunner.RecoverDrafts(context.Background())
	handler := api.New(svc, guarded, api.Options{
		// MCP streamable HTTP at /mcp, behind the same tokens (spec 3.1, 6.1).
		MCP:        mcp.NewHTTPHandler(mcpDeps),
		Agents:     agentsRunner,
		LocalAI:    localai.New(), // loopback Ollama only (free local AI)
		AdminToken: cfg.AdminToken,
		ProtectOps: !loopback,
		Ready:      cl.health.Ready, // fails once draining (E12)
		Webhooks:   &service.Webhooks{Store: st, Dispatcher: hooks},
		Events:     bus,
		Operator:   cl.operatorOptions(),
		Metrics:    cl.metrics(),
		AgentHook:  &agenthook.Admin{Store: st},
		Attendant:  attAdmin,
		Groups:     groups,
		Account:    acct,
		Social:     social,
		Menu:       menuSvc,
		// Marco 11: IP allowlists, scoped API keys, metadata, bulk, hosted pairing.
		Platform: &service.Platform{Store: st, Instances: svc},
		Pairing:  links,
		PairingPage: pairing.Handler(pairing.PageOptions{Links: links, Instances: svc,
			FrameAncestors: cfg.PairingFrameAncestors, Log: log}),
		Access: api.AccessOptions{TrustedProxies: trusted, AdminAllow: adminIPs, OperatorAllow: operatorIPs},
	}, log)
	var codes *dashboard.LaunchCodes
	var aiClients dashboard.AIConnector
	if loopback {
		codes = opt.LaunchCodes
		aiClients = opt.AIClients
	}
	srv := &http.Server{
		Handler: withDashboard(handler, !cfg.DisableDashboard,
			dashboard.Options{AdminToken: cfg.AdminToken, LaunchCodes: codes, RemoteCodes: cl.remoteCodes,
				AIClients: aiClients}),
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
	cl.serveOps()
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
	cl.drain() // /readyz answers 503 from now on
	hctx, cancel := context.WithTimeout(context.Background(), scaled(5*time.Second, scale))
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
// scale (<= 1) shrinks every phase to fit shutdown_timeout.
// agentTools is the MCP tool table of one instance (its own scope), as the agents use it.
func agentTools(d mcp.HTTPDeps, instanceID string, log *slog.Logger) []agents.ToolDef {
	defs := mcp.Catalog(d.Local(service.Scope{InstanceID: instanceID}), log)
	out := make([]agents.ToolDef, 0, len(defs))
	for _, t := range defs {
		out = append(out, agents.ToolDef{Name: t.Name, Description: t.Description, Schema: t.InputSchema, Call: t.Call})
	}
	return out
}

func shutdownAll(q *queue.Queues, mgr *manager.Manager, history *events.HistoryTracker, hooks *webhook.Dispatcher, scale float64) {
	qctx, qcancel := context.WithTimeout(context.Background(), scaled(10*time.Second, scale))
	q.Shutdown(qctx)
	qcancel()
	ctx, cancel := context.WithTimeout(context.Background(), scaled(manager.DefaultOptions().StopTimeout+5*time.Second, scale))
	mgr.Shutdown(ctx)
	cancel()
	history.Close()
	hctx, hcancel := context.WithTimeout(context.Background(), scaled(5*time.Second, scale))
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
		if cfg.RequireProxy && set.ProxyURL == "" {
			return nil, errProxyRequired
		}
		env := []string{"LITEZAP_WORKER_PROXY_URL=" + set.ProxyURL}
		if cfg.ZombieTimeout > 0 {
			env = append(env, "LITEZAP_WORKER_ZOMBIE_TIMEOUT="+cfg.ZombieTimeout.String())
		}
		return env, nil
	}
}

// menuAI is "Montar com IA" over the attendant's configured provider; its call errors
// become API errors (the provider's own message is not shown: it may carry details).
func menuAI(att *attendant.Attendant) service.MenuAI { return menuAIFrom(att.Extract) }

func menuAIFrom(extract service.MenuAI) service.MenuAI {
	return func(ctx context.Context, instanceID, system, user string, schema json.RawMessage) (string, error) {
		out, err := extract(ctx, instanceID, system, user, schema)
		if err == nil {
			return out, nil
		}
		var ce *agenthook.CallError
		if errors.As(err, &ce) {
			switch ce.Reason {
			case "not_configured":
				return "", apierr.New(http.StatusConflict, "ai_not_configured", ce.Detail, false)
			case "daily_cap":
				return "", apierr.New(http.StatusTooManyRequests, "rate_limited", ce.Detail, true)
			}
		}
		// Anything else (provider, store, timeout) is generic: its text may carry details.
		return "", apierr.New(http.StatusBadGateway, "ai_error", "the AI provider failed; try again", true)
	}
}

// catalogKnowledge lists an instance's own catalog as the attendant's product knowledge
// (knowledge_source "catalog"): name, price, description and availability.
func catalogKnowledge(r *service.CatalogReader) attendant.CatalogSource {
	return func(ctx context.Context, instanceID string) ([]attendant.KnowledgeEntry, error) {
		list, err := r.Products(ctx, instanceID, 200)
		if err != nil {
			return nil, err
		}
		out := make([]attendant.KnowledgeEntry, 0, len(list))
		for _, p := range list {
			if p.Hidden || p.Name == "" {
				continue // customers cannot see it: never offered
			}
			e := attendant.KnowledgeEntry{Kind: "product", Title: clipRunes(p.Name, 200), Content: clipRunes(p.Description, 3500)}
			if p.PriceAmount1000 > 0 {
				price := float64(p.PriceAmount1000) / 1000
				if p.SaleAmount1000 > 0 && p.SaleAmount1000 < p.PriceAmount1000 {
					price = float64(p.SaleAmount1000) / 1000
				}
				e.Price = &price
			}
			switch p.Availability {
			case "out_of_stock":
				e.Content = strings.TrimSpace(e.Content + " (esgotado)")
			case "in_stock", "":
			default:
				e.Content = strings.TrimSpace(e.Content + " (disponibilidade: " + p.Availability + ")")
			}
			if p.ReviewStatus != "" && p.ReviewStatus != "approved" {
				continue // pending or rejected: not visible to customers yet
			}
			out = append(out, e)
		}
		return out, nil
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
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
func withDashboard(apiHandler http.Handler, enabled bool, opt dashboard.Options) http.Handler {
	if !enabled {
		return apiHandler
	}
	mux := http.NewServeMux()
	mux.Handle(dashboard.Prefix, dashboard.Handler(opt))
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
