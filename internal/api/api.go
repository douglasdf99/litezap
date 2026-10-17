// Package api exposes the LiteZap REST API (spec section 6).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
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

type Options struct {
	AdminToken string
	// ProtectOps requires the admin token on /healthz and /readyz (set when not bound to loopback).
	ProtectOps bool
	Ready      func(context.Context) error
}

type server struct {
	svc InstanceService
	opt Options
	log *slog.Logger
}

type principal struct {
	admin      bool
	instanceID string
}

type ctxKey struct{}

func New(svc InstanceService, opt Options, log *slog.Logger) http.Handler {
	s := &server{svc: svc, opt: opt, log: log}
	r := chi.NewRouter()

	r.Group(func(r chi.Router) {
		if opt.ProtectOps {
			r.Use(s.authenticate, s.requireAdmin)
		}
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		r.Get("/readyz", s.readyz)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.With(s.requireAdmin).Post("/instances", s.createInstance)
		r.With(s.requireAdmin).Get("/instances", s.listInstances)
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
		})
	})
	return r
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || tok == "" {
			apierr.Write(w, apierr.Unauthorized())
			return
		}
		var p principal
		if secure.Equal(tok, s.opt.AdminToken) {
			p.admin = true
		} else {
			id, err := s.svc.Authenticate(r.Context(), tok)
			if err != nil {
				s.writeErr(w, err)
				return
			}
			p.instanceID = id
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
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

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return apierr.Validation("invalid JSON body")
	}
	return nil
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
	if err := decode(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	v, tok, err := s.svc.Create(r.Context(), req.Name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
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
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}

func (s *server) pairPhone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := decode(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	code, err := s.svc.PairPhone(r.Context(), chi.URLParam(r, "id"), req.Phone)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}
