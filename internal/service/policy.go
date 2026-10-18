package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
)

// Policy holds the managed-hosting rules for instances (cloud E4/E5). A nil *Policy
// imposes nothing, which is the self-host default.
type Policy struct {
	Store *store.Store
	// MaxInstances is the configured cap on instances created without the operator token
	// (0: none); the operator can override it at runtime (SetInstanceLimit).
	MaxInstances int
	// RequireProxy never starts a worker without a proxy (instance setting or
	// DefaultProxy).
	RequireProxy bool
	DefaultProxy string

	mu sync.Mutex // serializes the count-and-insert of Create
}

const kvMaxInstances = "max_instances"

// ProxyRequiredReason is the state_reason of an instance kept stopped for lack of a proxy.
const ProxyRequiredReason = "proxy_required"

// InstanceLimit is the effective cap (runtime override, else the configuration; 0: none)
// and whether it comes from the override.
func (p *Policy) InstanceLimit(ctx context.Context) (limit int, overridden bool, err error) {
	if p == nil {
		return 0, false, nil
	}
	v, err := p.Store.GetKV(ctx, kvMaxInstances)
	if errors.Is(err, store.ErrNotFound) {
		return p.MaxInstances, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return p.MaxInstances, false, nil
	}
	return n, true, nil
}

// SetInstanceLimit overrides the cap at runtime (persisted); nil drops the override.
func (p *Policy) SetInstanceLimit(ctx context.Context, n *int) error {
	if n == nil {
		return p.Store.DeleteKV(ctx, kvMaxInstances)
	}
	if *n < 0 {
		return apierr.Validation("max_instances must be 0 (no limit) or more")
	}
	return p.Store.SetKV(ctx, kvMaxInstances, strconv.Itoa(*n))
}

// admit checks the instance cap for the caller in ctx and, when it passes, holds the
// create lock until release is called (after the insert), so concurrent creates cannot
// overshoot it. The operator is never limited.
func (p *Policy) admit(ctx context.Context) (release func(), err error) {
	if p == nil {
		return func() {}, nil
	}
	p.mu.Lock()
	if sc, _ := ScopeFrom(ctx); sc.Operator {
		return p.mu.Unlock, nil
	}
	limit, _, err := p.InstanceLimit(ctx)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if limit > 0 {
		n, err := p.Store.CountInstances(ctx)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if n >= limit {
			p.mu.Unlock()
			return nil, apierr.New(http.StatusForbidden, "plan_limit",
				"instance limit reached ("+strconv.Itoa(limit)+"); upgrade the plan or delete an instance", false)
		}
	}
	return p.mu.Unlock, nil
}

// hasProxy reports whether the instance would start with a proxy.
func (p *Policy) hasProxy(in store.Instance) bool {
	if p.DefaultProxy != "" {
		return true
	}
	patch, err := settings.ParsePatch(in.SettingsJSON)
	return err == nil && patch.ProxyURL != nil && *patch.ProxyURL != ""
}

// checkProxy refuses to start an instance without a proxy when RequireProxy is set.
func (p *Policy) checkProxy(ctx context.Context, id string) error {
	if p == nil || !p.RequireProxy {
		return nil
	}
	in, err := p.Store.GetInstance(ctx, id)
	if err != nil {
		return mapErr(err)
	}
	if !p.hasProxy(in) {
		return proxyRequired()
	}
	return nil
}

func proxyRequired() error {
	return apierr.New(http.StatusConflict, ProxyRequiredReason,
		"this server requires a proxy: the operator must set proxy_url before the instance starts", false)
}

// holdWithoutProxy parks a just-created instance as stopped (reason proxy_required) when
// it has no proxy under RequireProxy; it reports whether it did.
func (p *Policy) holdWithoutProxy(ctx context.Context, id string) (bool, error) {
	if p == nil || !p.RequireProxy {
		return false, nil
	}
	in, err := p.Store.GetInstance(ctx, id)
	if err != nil {
		return false, err
	}
	if p.hasProxy(in) {
		return false, nil
	}
	return true, p.Store.UpdateInstanceState(ctx, id, instance.Stopped, ProxyRequiredReason)
}

// HoldUnproxied marks every instance that would boot without a proxy as stopped
// (reason proxy_required), so Boot does not start it. Called once before Boot.
func (p *Policy) HoldUnproxied(ctx context.Context) (int, error) {
	if p == nil || !p.RequireProxy {
		return 0, nil
	}
	list, err := p.Store.ListInstances(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, in := range list {
		if (in.State != instance.Created && !in.State.Active()) || p.hasProxy(in) { // Boot would not start it
			continue
		}
		if err := p.Store.UpdateInstanceState(ctx, in.ID, instance.Stopped, ProxyRequiredReason); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// CheckProxyPatch refuses a settings change that would leave a running-capable instance
// without a proxy under RequireProxy (clearing proxy_url with no default proxy).
func (p *Policy) CheckProxyPatch(patch settings.Patch) error {
	if p == nil || !p.RequireProxy || p.DefaultProxy != "" || patch.ProxyURL == nil || *patch.ProxyURL != "" {
		return nil
	}
	return apierr.Validation("this server requires a proxy: proxy_url cannot be cleared")
}
