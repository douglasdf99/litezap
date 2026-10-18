// Package api exposes the LiteZap REST API (spec section 6).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"
)

// InstanceService is the subset of service.Instances used by the API.
type InstanceService interface {
	Create(ctx context.Context, name string) (service.InstanceView, string, error)
	List(ctx context.Context) ([]service.InstanceView, error)
	Get(ctx context.Context, id string) (service.InstanceView, error)
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Logout(ctx context.Context, id string) error
	RotateToken(ctx context.Context, id string) (string, error)
	QR(ctx context.Context, id string) (string, error)
	PairPhone(ctx context.Context, id, phone string) (string, error)
	Authenticate(ctx context.Context, token string) (string, error)
}

// MessagingService is the subset of service.Messaging used by the API.
type MessagingService interface {
	Send(ctx context.Context, instanceID string, in service.SendInput, wait time.Duration) (service.MessageView, error)
	Wait(ctx context.Context, instanceID, id string, wait time.Duration) (service.MessageView, error)
	GetMessage(ctx context.Context, instanceID, id string) (service.MessageView, error)
	Chats(ctx context.Context, instanceID string, limit int, cursor string) (service.ChatPage, error)
	ChatMessages(ctx context.Context, instanceID, chat string, limit int, cursor string) (service.MessagePage, error)
	MarkRead(ctx context.Context, instanceID, chat string) error
	Contacts(ctx context.Context, instanceID string) ([]service.ContactView, error)
	Contact(ctx context.Context, instanceID, jid string) (service.ContactView, error)
	CheckNumbers(ctx context.Context, instanceID string, phones []string) ([]service.NumberView, error)
	Settings(ctx context.Context, instanceID string) (settings.Settings, error)
	UpdateSettings(ctx context.Context, instanceID string, patch settings.Patch) (settings.Settings, error)
	Media(ctx context.Context, instanceID, mediaID string) (service.MediaFile, error)
	SignedMediaURL(ctx context.Context, instanceID, mediaID string) (string, time.Time, error)
	VerifyMediaSignature(instanceID, mediaID, exp, sig string) bool
	Queue(ctx context.Context, instanceID, status string, limit int, cursor string) (service.QueuePage, error)
	CancelQueued(ctx context.Context, instanceID, id string) (service.MessageView, error)
	ClearQueue(ctx context.Context, instanceID string) (int, error)
	SubscribePresence(ctx context.Context, instanceID, to string) error
}

type Options struct {
	AdminToken string
	// ProtectOps requires the admin token on /healthz and /readyz (set when not bound to loopback).
	ProtectOps bool
	Ready      func(context.Context) error
	// Webhooks, when set, serves /v1/webhooks.
	Webhooks WebhookService
	// Events, when set, serves the SSE streams.
	Events *events.Bus
	// SSEHeartbeat is the interval of keep-alive comments (default 15s); SSEBuffer is how
	// many events a stream may lag behind before it is disconnected (default 256).
	SSEHeartbeat time.Duration
	SSEBuffer    int
	// MCP, when set, serves the MCP streamable HTTP endpoint at /mcp behind the same
	// Bearer authentication; the caller's service.Scope is in the request context.
	MCP http.Handler
	// Metrics, when set, serves GET /metrics (admin or operator token).
	Metrics http.Handler
	// Operator configures the managed-hosting operator role (zero value: off).
	Operator OperatorOptions
	// AgentHook, when set, serves the agent hook routes (spec 4.3).
	AgentHook AgentHookService
}

type server struct {
	svc InstanceService
	msg MessagingService
	opt Options
	log *slog.Logger
	// bodySlots bounds concurrent large send bodies (memory).
	bodySlots chan struct{}
}

type principal struct {
	admin      bool
	operator   bool // managed hosting: above the admin (always also admin)
	instanceID string
}

type ctxKey struct{}

func New(svc InstanceService, msg MessagingService, opt Options, log *slog.Logger) http.Handler {
	if opt.SSEHeartbeat == 0 {
		opt.SSEHeartbeat = 15 * time.Second
	}
	if opt.SSEBuffer == 0 {
		opt.SSEBuffer = 256
	}
	s := &server{svc: svc, msg: msg, opt: opt, log: log, bodySlots: make(chan struct{}, 3)}
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		if opt.ProtectOps {
			r.Use(s.authenticate, s.requireAdmin)
		}
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		r.Get("/readyz", s.readyz)
	})
	// Metrics always need the admin token here, even on loopback (a reverse proxy on the
	// same host would expose them); ops_listen_addr serves them without a token.
	if opt.Metrics != nil {
		r.With(s.authenticate, s.requireAdmin).Method(http.MethodGet, "/metrics", opt.Metrics)
	}

	// Signed media URLs work without a token (spec 4.5); the signature is the credential.
	r.Get("/media/{id}/{media_id}", s.signedMedia)

	if opt.MCP != nil {
		r.With(s.authenticate).Handle("/mcp", opt.MCP)
	}

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.Get("/me", s.me)
		r.With(s.requireAdmin).Post("/instances", s.createInstance)
		r.With(s.requireAdmin).Get("/instances", s.listInstances)
		s.operatorRoutes(r)
		if opt.Webhooks != nil {
			r.Route("/webhooks", s.webhookRoutes)
		}
		if opt.Events != nil {
			r.With(s.requireAdmin).Get("/events", s.allEvents)
		}
		r.Route("/instances/{id}", func(r chi.Router) {
			r.Use(s.requireInstance)
			r.Get("/", s.getInstance)
			r.With(s.requireAdmin).Delete("/", s.deleteInstance)
			r.Post("/start", s.action(svc.Start))
			r.Post("/stop", s.action(svc.Stop))
			r.Post("/restart", s.action(svc.Restart))
			r.Post("/logout", s.action(svc.Logout))
			r.With(s.requireAdmin).Post("/token/rotate", s.rotateToken)
			r.Get("/qr", s.qr)
			r.Post("/pair-phone", s.pairPhone)

			r.Get("/settings", s.getSettings)
			r.Put("/settings", s.putSettings)
			r.Post("/messages", s.sendMessage)
			r.Get("/messages/{msg_id}", s.getMessage)
			r.Get("/queue", s.listQueue)
			r.Delete("/queue", s.clearQueue)
			r.Delete("/queue/{msg_id}", s.cancelQueued)
			r.Get("/chats", s.listChats)
			r.Get("/chats/{jid}/messages", s.chatMessages)
			r.Post("/chats/{jid}/read", s.markRead)
			r.Get("/contacts", s.listContacts)
			r.Get("/contacts/{jid}", s.getContact)
			r.Post("/check-numbers", s.checkNumbers)
			r.Get("/media/{media_id}", s.getMedia)
			s.agentRoutes(r)
			r.Post("/presence/subscribe", s.subscribePresence)
			if opt.Events != nil {
				r.Get("/events", s.instanceEvents)
			}
		})
	})
	return r
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			apierr.Write(w, apierr.Unauthorized())
			return
		}
		var p principal
		if s.isOperator(tok) {
			p.admin, p.operator = true, true
		} else if secure.Equal(tok, s.opt.AdminToken) {
			p.admin = true
		} else {
			id, err := s.svc.Authenticate(r.Context(), tok)
			if err != nil {
				s.writeErr(w, err)
				return
			}
			p.instanceID = id
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, p)
		ctx = service.WithScope(ctx, p.scope())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the token of a "Bearer" Authorization header; the scheme is
// case-insensitive (RFC 9110 section 11.1).
func bearerToken(auth string) (string, bool) {
	const scheme = "Bearer "
	if len(auth) <= len(scheme) || !strings.EqualFold(auth[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(auth[len(scheme):])
	return tok, tok != ""
}

func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(ctxKey{}).(principal)
	return p
}

func (s *server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r).admin {
			apierr.Write(w, apierr.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) requireInstance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if !p.admin && p.instanceID != chi.URLParam(r, "id") {
			apierr.Write(w, apierr.Forbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeErr logs unexpected errors before rendering them as internal.
func (s *server) writeErr(w http.ResponseWriter, err error) {
	var e *apierr.Error
	if !errors.As(err, &e) {
		s.log.Error("request failed", "err", err)
	}
	apierr.Write(w, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// noStore marks a response that carries a secret (token, QR) as not cacheable.
func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeMax(w, r, v, 1<<20)
}

func decodeMax(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apierr.New(http.StatusRequestEntityTooLarge, "media_too_large",
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit), false)
		}
		return apierr.Validation("invalid JSON body")
	}
	return nil
}

// me tells the caller who its token is (used by `litezap mcp` to learn its scope).
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	out := map[string]any{"admin": p.admin, "role": p.role()}
	if p.instanceID != "" {
		out["instance_id"] = p.instanceID
	}
	writeJSON(w, 200, out)
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.opt.Ready != nil {
		if err := s.opt.Ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (s *server) createInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	v, tok, err := s.svc.Create(r.Context(), req.Name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, http.StatusCreated, map[string]any{"instance": v, "token": tok})
}

func (s *server) listInstances(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"instances": list})
}

func (s *server) getInstance(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// action runs a lifecycle operation and returns the instance as it is right after.
func (s *server) action(fn func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := fn(r.Context(), id); err != nil {
			s.writeErr(w, err)
			return
		}
		v, err := s.svc.Get(r.Context(), id)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func (s *server) rotateToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.svc.RotateToken(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, 200, map[string]string{"token": tok})
}

func (s *server) qr(w http.ResponseWriter, r *http.Request) {
	code, err := s.svc.QR(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if r.URL.Query().Get("format") == "png" {
		png, err := qrcode.Encode(code, qrcode.Medium, 320)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
		return
	}
	noStore(w)
	writeJSON(w, 200, map[string]string{"code": code})
}

func (s *server) pairPhone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	code, err := s.svc.PairPhone(r.Context(), chi.URLParam(r, "id"), req.Phone)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, 200, map[string]string{"code": code})
}
