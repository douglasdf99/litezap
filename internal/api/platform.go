package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/ipallow"
	"github.com/douglasdf99/litezap/internal/pairing"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// PlatformService is the marco 11 service (service.Platform).
type PlatformService interface {
	Authenticate(ctx context.Context, token string) (service.Auth, error)
	KeyAuth(ctx context.Context, keyID string) (service.Auth, error)
	Touch(ctx context.Context, keyID string)
	Update(ctx context.Context, id string, patch service.InstancePatch) (service.InstanceView, error)
	Search(ctx context.Context, q service.InstanceQuery) (service.InstancePage, error)
	Bulk(ctx context.Context, req service.BulkRequest) ([]service.BulkResult, error)
	CreateKey(ctx context.Context, instanceID string, in service.KeyInput) (service.KeyView, string, error)
	ListKeys(ctx context.Context, instanceID string) ([]service.KeyView, error)
	DeleteKey(ctx context.Context, instanceID, keyID string) error
}

// PairingService mints and revokes hosted pairing links (pairing.Links).
type PairingService interface {
	Mint(ctx context.Context, instanceID string, ttl time.Duration) (pairing.Link, error)
	Revoke(ctx context.Context, instanceID string) (int, error)
}

// AccessOptions are the server-wide IP allowlists (marco 11). Empty lists allow any
// address; TrustedProxies are the reverse proxies whose X-Forwarded-For is believed.
type AccessOptions struct {
	TrustedProxies ipallow.List
	AdminAllow     ipallow.List
	OperatorAllow  ipallow.List
}

var errIPNotAllowed = apierr.New(http.StatusForbidden, "forbidden", "this token is not allowed from your IP address", false)

// checkIP enforces the allowlists of the authenticated principal: the operator or admin
// list, or the credential's own list plus, for an API key, the instance's list.
func (s *server) checkIP(r *http.Request, p principal, a service.Auth) error {
	var lists []ipallow.List
	switch {
	case p.operator:
		lists = []ipallow.List{s.opt.Access.OperatorAllow}
	case p.admin:
		lists = []ipallow.List{s.opt.Access.AdminAllow}
	default:
		lists = []ipallow.List{a.IPAllowlist, a.InstanceAllowlist}
	}
	ip := ipallow.ClientIP(r, s.opt.Access.TrustedProxies)
	for _, l := range lists {
		if len(l) > 0 && !l.Contains(ip) {
			return errIPNotAllowed
		}
	}
	return nil
}

// keyStillValid re-checks an API key during a long request (SSE): revoked, expired or
// now outside an allowlist ends the stream. Other principals always pass.
func (s *server) keyStillValid(r *http.Request) bool {
	p := principalFrom(r)
	if p.keyID == "" || s.opt.Platform == nil {
		return true
	}
	a, err := s.opt.Platform.KeyAuth(r.Context(), p.keyID)
	return err == nil && s.checkIP(r, p, a) == nil
}

// Routes an API key may call, by scope. Anything else is refused (fail closed): no
// lifecycle, settings, QR, webhooks, agent hook, group invites, MCP or admin routes; no
// status, channel or label writes (marco 10).
var (
	keyReadRe = regexp.MustCompile(`^/v1/(me|instances/[^/]+/?|instances/[^/]+/(messages/[^/]+|queue|chats|chats/[^/]+|chats/[^/]+/messages|` +
		`contacts|contacts/[^/]+|contacts/[^/]+/(picture|profile)|media/[^/]+|groups|groups/[^/]+|communities|communities/[^/]+|` +
		`communities/[^/]+/groups|blocklist|profile|profile/picture|privacy|events|usage|` +
		// marco 10 reads (no secrets): statuses, channels, labels, business profile
		`status|status/privacy|channels|channels/[^/]+|channels/[^/]+/messages|labels|business/profile))$`)
	keySendPostRe = regexp.MustCompile(`^/v1/instances/[^/]+/messages$`)
	keySendGetRe  = regexp.MustCompile(`^/v1/(me|instances/[^/]+/messages/[^/]+)$`)
)

// routePath is the path chi routes on (the raw path when the URL has escapes), so the
// scope check sees the same segments as the router.
func routePath(r *http.Request) string {
	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}
	return r.URL.Path
}

// keyAllows reports whether a key with these scopes may make the request.
func keyAllows(scopes []string, r *http.Request) bool {
	path := routePath(r)
	if slices.Contains(scopes, service.ScopeRead) && r.Method == http.MethodGet && keyReadRe.MatchString(path) {
		return true
	}
	if slices.Contains(scopes, service.ScopeSend) {
		if r.Method == http.MethodPost && keySendPostRe.MatchString(path) {
			return true
		}
		if r.Method == http.MethodGet && keySendGetRe.MatchString(path) {
			return true
		}
	}
	return false
}

// enforceKeyScope limits API keys to the routes of their scopes.
func (s *server) enforceKeyScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := principalFrom(r); p.keyID != "" && !keyAllows(p.scopes, r) {
			apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "this API key's scopes do not allow this request", false))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// denyKeys refuses API keys (used on /mcp, whose tools are not scope-checked).
func (s *server) denyKeys(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r).keyID != "" {
			apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "API keys cannot use this endpoint; use the instance token", false))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) platformRoutes(r chi.Router) {
	if s.opt.Platform != nil {
		r.With(s.requireAdmin).Post("/instances/bulk", s.bulk)
	}
}

func (s *server) instancePlatformRoutes(r chi.Router) {
	if s.opt.Platform != nil {
		r.Patch("/", s.patchInstance)
		r.Get("/api-keys", s.listKeys)
		r.Post("/api-keys", s.createKey)
		r.Delete("/api-keys/{key_id}", s.deleteKey)
	}
	if s.opt.Operator.Usage != nil {
		r.Get("/usage", s.instanceUsage)
	}
	if s.opt.Pairing != nil {
		r.With(s.requireAdmin).Post("/pairing-links", s.mintPairingLink)
		r.With(s.requireAdmin).Delete("/pairing-links", s.revokePairingLinks)
	}
}

func (s *server) patchInstance(w http.ResponseWriter, r *http.Request) {
	var patch service.InstancePatch
	if err := decode(w, r, &patch); err != nil {
		s.writeErr(w, err)
		return
	}
	if (patch.Tags != nil || patch.IPAllowlist != nil) && !principalFrom(r).admin {
		s.writeErr(w, apierr.New(http.StatusForbidden, "forbidden", "tags and ip_allowlist need the admin token", false))
		return
	}
	v, err := s.opt.Platform.Update(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// searchInstances serves GET /instances with filters (state, tag, q) and pagination.
func (s *server) searchInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var in service.InstanceQuery
	for _, v := range q["state"] {
		for _, st := range strings.Split(v, ",") {
			if st = strings.TrimSpace(st); st != "" {
				in.States = append(in.States, st)
			}
		}
	}
	in.Tag, in.Search, in.Cursor = q.Get("tag"), q.Get("q"), q.Get("cursor")
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeErr(w, apierr.Validation("limit must be a positive number"))
			return
		}
		in.Limit = n
	}
	page, err := s.opt.Platform.Search(r.Context(), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) bulk(w http.ResponseWriter, r *http.Request) {
	var req service.BulkRequest
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.opt.Platform.Bulk(r.Context(), req)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	failed := 0
	for _, x := range res {
		if !x.OK {
			failed++
		}
	}
	writeJSON(w, 200, map[string]any{"action": req.Action, "results": res, "succeeded": len(res) - failed, "failed": failed})
}

func (s *server) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.opt.Platform.ListKeys(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"api_keys": keys})
}

func (s *server) createKey(w http.ResponseWriter, r *http.Request) {
	var in service.KeyInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	in.CreatedByAdmin = principalFrom(r).admin
	k, secret, err := s.opt.Platform.CreateKey(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": k, "key": secret})
}

func (s *server) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Platform.DeleteKey(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "key_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) instanceUsage(w http.ResponseWriter, r *http.Request) {
	from, to, err := usageRange(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.svc.Get(r.Context(), id); err != nil {
		s.writeErr(w, err)
		return
	}
	rows, err := s.opt.Operator.Usage.Usage(r.Context(), from, to, id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"instance_id": id, "from": from, "to": to, "usage": rows})
}

func (s *server) mintPairingLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	if r.ContentLength != 0 {
		if err := decode(w, r, &req); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	link, err := s.opt.Pairing.Mint(r.Context(), chi.URLParam(r, "id"), time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, link)
}

func (s *server) revokePairingLinks(w http.ResponseWriter, r *http.Request) {
	n, err := s.opt.Pairing.Revoke(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"revoked": n})
}
