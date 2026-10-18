package serve

import (
	"context"
	"errors"
	"log/slog"

	"github.com/douglasdf99/litezap/internal/api"
	"github.com/douglasdf99/litezap/internal/config"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

// cloud holds the managed-hosting pieces (cloud spec section 9). With the default
// configuration they impose nothing: no operator token, no cap, no lock, no suspension.
type cloud struct {
	cfg     config.Config
	log     *slog.Logger
	policy  *service.Policy
	sending *service.Sending
}

func newCloud(cfg config.Config, st *store.Store, log *slog.Logger) *cloud {
	return &cloud{cfg: cfg, log: log,
		policy:  &service.Policy{Store: st, MaxInstances: cfg.MaxInstances, RequireProxy: cfg.RequireProxy, DefaultProxy: cfg.DefaultProxyURL},
		sending: &service.Sending{Store: st},
	}
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

// operatorOptions configures the API's operator role.
func (c *cloud) operatorOptions() api.OperatorOptions {
	return api.OperatorOptions{
		Tokens: c.cfg.OperatorTokens(), Locked: c.cfg.LockedSettings,
		Policy: c.policy, Sending: c.sending, PublicURL: c.cfg.PublicURL,
	}
}

// errProxyRequired is returned by workerEnv when require_proxy forbids a direct connection.
var errProxyRequired = errors.New("require_proxy: refusing to start a worker without a proxy")
