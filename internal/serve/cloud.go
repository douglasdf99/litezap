package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/dashboard"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/export"
	"github.com/douglasdf99/litezap/internal/ops"
	"github.com/douglasdf99/litezap/internal/retention"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/usage"
)

// Version is reported by /metrics (litezap_build_info); set by the main package.
var Version = "dev"

// cloud holds the managed-hosting pieces (cloud spec section 9). With the default
// configuration they impose nothing: no operator token, no cap, no lock, no suspension,
// no ops listener; usage counting and /readyz draining are always on (they change no
// API behavior).
type cloud struct {
	cfg     config.Config
	log     *slog.Logger
	st      *store.Store
	policy  *service.Policy
	sending *service.Sending
	usage   *usage.Recorder
	proxy   *usage.ProxyWatch
	health  *ops.Health
	sweeper *retention.Sweeper

	// remoteCodes are the one-click dashboard logins minted by the operator (E7); nil
	// without an operator token or with the dashboard disabled.
	remoteCodes *dashboard.LaunchCodes

	opsLn  net.Listener
	opsSrv *http.Server
}

func newCloud(cfg config.Config, st *store.Store, log *slog.Logger) *cloud {
	c := &cloud{cfg: cfg, log: log, st: st,
		policy:  &service.Policy{Store: st, MaxInstances: cfg.MaxInstances, RequireProxy: cfg.RequireProxy, DefaultProxy: cfg.DefaultProxyURL},
		sending: &service.Sending{Store: st},
		usage:   &usage.Recorder{Store: st, Log: log},
		proxy:   &usage.ProxyWatch{},
		health:  &ops.Health{Ping: st.Ping},
		sweeper: &retention.Sweeper{Store: st, Log: log, Media: cfg.MediaRetentionDays > 0,
			Messages: time.Duration(cfg.MessageRetentionDays) * 24 * time.Hour},
	}
	if !cfg.DisableDashboard && len(cfg.OperatorTokens()) > 0 {
		c.remoteCodes = dashboard.NewRemoteLaunchCodes()
	}
	return c
}

// listenOps binds ops_listen_addr (if set) before any worker is touched, like the API.
func (c *cloud) listenOps() error {
	if c.cfg.OpsListenAddr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", c.cfg.OpsListenAddr)
	if err != nil {
		return fmt.Errorf("listen ops %s: %w", c.cfg.OpsListenAddr, err)
	}
	c.opsLn = ln
	return nil
}

// attach connects the pieces that need the event bus and starts the background loops.
func (c *cloud) attach(bus *events.Bus) {
	c.proxy.Bus = bus
	c.usage.Start()
	c.proxy.Start()
	c.sweeper.Start() // no-op unless a retention is configured
}

// mediaRetention is how long a downloaded attachment is kept locally (0: the default
// expiry, which nothing enforces unless media_retention_days is set).
func (c *cloud) mediaRetention() time.Duration {
	return time.Duration(c.cfg.MediaRetentionDays) * 24 * time.Hour
}

// beforeBoot loads the persisted suspensions and parks instances that would start
// without a required proxy.
func (c *cloud) beforeBoot(ctx context.Context) error {
	if err := c.sending.Load(ctx); err != nil {
		return err
	}
	n, err := c.policy.HoldUnproxied(ctx)
	if n > 0 {
		c.log.Warn("instances without a proxy kept stopped (require_proxy)", "count", n)
	}
	return err
}

func (c *cloud) metrics() http.Handler {
	return ops.Metrics(ops.Source{Store: c.st, Usage: c.usage, Proxy: c.proxy, Health: c.health,
		Sending: c.sending, Version: Version})
}

// serveOps starts the unauthenticated ops listener, if bound.
func (c *cloud) serveOps() {
	if c.opsLn == nil {
		return
	}
	c.opsSrv = &http.Server{Handler: ops.Handler(c.health, c.metrics()), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	c.log.Info("ops endpoints listening (no token: keep this address private)", "addr", c.opsLn.Addr().String())
	go func() {
		if err := c.opsSrv.Serve(c.opsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("ops listener", "err", err)
		}
	}()
}

// operatorOptions configures the API's operator role.
func (c *cloud) operatorOptions() api.OperatorOptions {
	o := api.OperatorOptions{
		Tokens: c.cfg.OperatorTokens(), Locked: c.cfg.LockedSettings,
		Policy: c.policy, Sending: c.sending, Usage: c.usage, PublicURL: c.cfg.PublicURL,
		Export: &export.Exporter{Store: c.st, DataDir: c.cfg.DataDir, Version: Version, Log: c.log},
	}
	if c.remoteCodes != nil {
		o.MintLogin = c.remoteCodes.MintWithExpiry
	}
	return o
}

// drain makes /readyz fail as soon as the shutdown starts (E12).
func (c *cloud) drain() { c.health.Drain() }

// close stops the loops and writes the pending usage; call it after the workers stopped
// (their last state changes count) and before the store closes. The ops listener goes
// last so health checks keep answering during the shutdown.
func (c *cloud) close(ctx context.Context) {
	c.sweeper.Close()
	c.proxy.Close()
	if err := c.usage.Close(ctx); err != nil {
		c.log.Error("usage: final flush", "err", err)
	}
	if c.opsSrv != nil {
		_ = c.opsSrv.Shutdown(ctx)
		_ = c.opsSrv.Close()
	} else if c.opsLn != nil {
		_ = c.opsLn.Close()
	}
}

// defaultShutdownTotal is the worst case of the default shutdown phases: HTTP 5s, send
// queue 10s, workers 15s, webhooks 5s, usage flush and ops listener 5s.
const defaultShutdownTotal = 40 * time.Second

// shutdownScale shrinks the default shutdown phases to fit
// shutdown_timeout, e.g. inside a platform's kill timeout; 1 when unset or larger.
func shutdownScale(timeout time.Duration) float64 {
	if timeout <= 0 || timeout >= defaultShutdownTotal {
		return 1
	}
	return float64(timeout) / float64(defaultShutdownTotal)
}

func scaled(d time.Duration, f float64) time.Duration { return time.Duration(float64(d) * f) }

// errProxyRequired is returned by workerEnv when require_proxy forbids a direct connection.
var errProxyRequired = errors.New("require_proxy: refusing to start a worker without a proxy")
