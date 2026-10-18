package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/go-chi/chi/v5"
)

// OperatorOptions configure the managed-hosting operator role (cloud spec, E4/E5/E7/E8/
// E11/E17). The operator token is held by the control plane only; the customer may hold
// the admin token, which then cannot change the locked settings, exceed the instance cap
// or call /v1/operator/*. The zero value turns all of it off.
type OperatorOptions struct {
	// Tokens are the accepted operator tokens (two during a rotation).
	Tokens []string
	// Locked lists the settings only the operator may change ("proxy_url", "unsafe_pacing").
	Locked []string
	// Policy holds the instance cap and the required-proxy rule.
	Policy *service.Policy
	// Sending, when set, serves the send-suspension endpoints.
	Sending SendingControl
	// Usage, when set, serves GET /v1/usage.
	Usage UsageReader
	// Export, when set, serves POST /v1/export (admin or operator).
	Export http.Handler
	// MintLogin, when set, serves POST /v1/operator/dashboard-login: a single-use code
	// that the dashboard exchanges for the admin token. PublicURL builds the login link.
	MintLogin func() (code string, expires time.Time)
	PublicURL string
}

// SendingControl suspends and releases sending (E17).
type SendingControl interface {
	Status(ctx context.Context) (service.SendingStatus, error)
	SetGlobal(ctx context.Context, suspended bool, reason string) error
	SetInstance(ctx context.Context, id string, suspended bool, reason string) error
}

// UsageReader returns the persisted usage counters (E11).
type UsageReader interface {
	Usage(ctx context.Context, from, to, instanceID string) ([]store.Usage, error)
}

func (s *server) isOperator(tok string) bool {
	ok := false
	for _, t := range s.opt.Operator.Tokens {
		if t != "" && secure.Equal(tok, t) {
			ok = true // no early return: compare against every token
		}
	}
	return ok
}

func (p principal) scope() service.Scope {
	return service.Scope{Admin: p.admin, Operator: p.operator, InstanceID: p.instanceID}
}

func (p principal) role() string {
	switch {
	case p.operator:
		return "operator"
	case p.admin:
		return "admin"
	}
	return "instance"
}

func (s *server) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r).operator {
			apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "this endpoint needs the operator token", false))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) locked(field string) bool { return slices.Contains(s.opt.Operator.Locked, field) }

// checkSettingsPatch enforces who may change proxy_url and unsafe_pacing.
func (s *server) checkSettingsPatch(r *http.Request, patch settings.Patch) error {
	p := principalFrom(r)
	for field, set := range map[string]bool{"proxy_url": patch.ProxyURL != nil, "unsafe_pacing": patch.UnsafePacing != nil} {
		if !set {
			continue
		}
		if !p.admin {
			return apierr.New(http.StatusForbidden, "forbidden", "proxy_url and unsafe_pacing need the admin token", false)
		}
		if s.locked(field) && !p.operator {
			return apierr.New(http.StatusForbidden, "forbidden", field+" is managed by the operator of this server", false)
		}
	}
	return s.opt.Operator.Policy.CheckProxyPatch(patch)
}

type settingsOut struct {
	settings.Settings
	// Locked lists the settings this caller cannot change because the operator manages them.
	Locked []string `json:"locked,omitempty"`
}

// settingsView adds the locked fields and, for anyone but the operator, hides the
// credentials and user name of a locked proxy (only its scheme and host stay visible).
func (s *server) settingsView(r *http.Request, set settings.Settings) settingsOut {
	out := settingsOut{Settings: set}
	if principalFrom(r).operator {
		return out
	}
	out.Locked = s.opt.Operator.Locked
	if s.locked("proxy_url") && set.ProxyURL != "" {
		out.ProxyURL = proxyHostOnly(set.ProxyURL)
	}
	return out
}

func proxyHostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[managed]"
	}
	return u.Scheme + "://" + u.Host
}

func (s *server) operatorRoutes(r chi.Router) {
	op := s.opt.Operator
	if op.Usage != nil {
		r.With(s.requireOperator).Get("/usage", s.usage)
	}
	if op.Export != nil {
		r.With(s.requireAdmin).Method(http.MethodPost, "/export", op.Export)
	}
	if len(op.Tokens) == 0 {
		return
	}
	r.Route("/operator", func(r chi.Router) {
		r.Use(s.requireOperator)
		if op.Policy != nil {
			r.Get("/limits", s.getLimits)
			r.Put("/limits", s.putLimits)
		}
		if op.Sending != nil {
			r.Get("/sending", s.getSending)
			r.Put("/sending", s.putGlobalSending)
			r.Put("/instances/{id}/sending", s.putInstanceSending)
		}
		if op.MintLogin != nil {
			r.Post("/dashboard-login", s.dashboardLogin)
		}
	})
}

func (s *server) limitsBody(ctx context.Context) (map[string]any, error) {
	p := s.opt.Operator.Policy
	limit, overridden, err := p.InstanceLimit(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"max_instances": limit, "configured_max_instances": p.MaxInstances,
		"overridden": overridden, "instances": len(all)}, nil
}

func (s *server) getLimits(w http.ResponseWriter, r *http.Request) {
	out, err := s.limitsBody(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// putLimits sets the runtime instance cap; {"max_instances": null} returns to the
// configured value. Lowering it below the current count only blocks new creates.
func (s *server) putLimits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxInstances *int `json:"max_instances"`
	}
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Operator.Policy.SetInstanceLimit(r.Context(), req.MaxInstances); err != nil {
		s.writeErr(w, err)
		return
	}
	s.getLimits(w, r)
}

type sendingReq struct {
	Suspended *bool  `json:"suspended"`
	Reason    string `json:"reason"`
}

func (s *server) decodeSending(w http.ResponseWriter, r *http.Request) (sendingReq, bool) {
	var req sendingReq
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return req, false
	}
	if req.Suspended == nil {
		s.writeErr(w, apierr.Validation("suspended (true or false) is required"))
		return req, false
	}
	if len(req.Reason) > 200 {
		s.writeErr(w, apierr.Validation("reason is limited to 200 characters"))
		return req, false
	}
	return req, true
}

func (s *server) getSending(w http.ResponseWriter, r *http.Request) {
	st, err := s.opt.Operator.Sending.Status(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *server) putGlobalSending(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeSending(w, r)
	if !ok {
		return
	}
	if err := s.opt.Operator.Sending.SetGlobal(r.Context(), *req.Suspended, req.Reason); err != nil {
		s.writeErr(w, err)
		return
	}
	s.getSending(w, r)
}

func (s *server) putInstanceSending(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeSending(w, r)
	if !ok {
		return
	}
	if err := s.opt.Operator.Sending.SetInstance(r.Context(), chi.URLParam(r, "id"), *req.Suspended, req.Reason); err != nil {
		s.writeErr(w, err)
		return
	}
	s.getSending(w, r)
}

const dayLayout = "2006-01-02"

// usage returns daily counters; from and to are UTC days (default: the last 31 days).
func (s *server) usage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := time.Now().UTC()
	from, to := q.Get("from"), q.Get("to")
	if to == "" {
		to = today.Format(dayLayout)
	}
	if from == "" {
		from = today.AddDate(0, 0, -30).Format(dayLayout)
	}
	f, err1 := time.Parse(dayLayout, from)
	t, err2 := time.Parse(dayLayout, to)
	if err1 != nil || err2 != nil || t.Before(f) {
		s.writeErr(w, apierr.Validation("from and to must be days (YYYY-MM-DD) with from <= to"))
		return
	}
	if t.Sub(f) > 366*24*time.Hour {
		s.writeErr(w, apierr.Validation("the range is limited to 366 days"))
		return
	}
	rows, err := s.opt.Operator.Usage.Usage(r.Context(), from, to, strings.TrimSpace(q.Get("instance_id")))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "usage": rows})
}

// dashboardLogin mints a one-click login code for the embedded dashboard. The code is
// exchanged (once, before it expires) for the admin token, never the operator token.
func (s *server) dashboardLogin(w http.ResponseWriter, r *http.Request) {
	code, exp := s.opt.Operator.MintLogin()
	out := map[string]any{"code": code, "expires_at": exp.UTC()}
	if base := strings.TrimRight(s.opt.Operator.PublicURL, "/"); base != "" {
		out["url"] = base + "/app/#code=" + code
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, out)
}
