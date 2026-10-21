package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/douglasdf99/litezap/internal/localai"
	"github.com/go-chi/chi/v5"
)

// LocalAIService detects the loopback Ollama and downloads models into it;
// localai.Service implements it. The Ollama address is fixed by the engine: requests
// never choose it.
type LocalAIService interface {
	Status(ctx context.Context) localai.Status
	StartPull(ctx context.Context, model string) (localai.PullStatus, error)
	PullStatus() *localai.PullStatus
	CancelPull()
}

// aiRoutes mounts /v1/ai (admin only): the free provider presets and the local Ollama.
func (s *server) aiRoutes(r chi.Router) {
	if s.opt.LocalAI == nil && s.opt.Attendant == nil {
		return
	}
	r.Route("/ai", func(r chi.Router) {
		r.Use(s.requireAdmin)
		r.Get("/presets", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"presets": attendant.ProviderPresets()})
		})
		if s.opt.LocalAI == nil {
			return
		}
		r.Get("/local", func(w http.ResponseWriter, r *http.Request) {
			noStore(w)
			writeJSON(w, 200, s.opt.LocalAI.Status(r.Context()))
		})
		r.Post("/local/pull", s.startPull)
		r.Get("/local/pull", func(w http.ResponseWriter, _ *http.Request) {
			noStore(w)
			writeJSON(w, 200, map[string]any{"pull": s.opt.LocalAI.PullStatus()})
		})
		r.Delete("/local/pull", func(w http.ResponseWriter, _ *http.Request) {
			s.opt.LocalAI.CancelPull()
			w.WriteHeader(http.StatusNoContent)
		})
	})
}

func (s *server) startPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string `json:"model"`
	}
	if err := decodeStrictMax(w, r, &in, 4<<10); err != nil {
		s.writeErr(w, err)
		return
	}
	p, err := s.opt.LocalAI.StartPull(r.Context(), in.Model)
	switch {
	case errors.Is(err, localai.ErrBadModel):
		s.writeErr(w, apierr.Validation(err.Error()))
		return
	case errors.Is(err, localai.ErrPullRunning):
		s.writeErr(w, apierr.New(http.StatusConflict, "pull_running", err.Error(), true))
		return
	case errors.Is(err, localai.ErrUnreachable):
		s.writeErr(w, apierr.New(http.StatusServiceUnavailable, "ollama_unreachable", err.Error(), true))
		return
	case err != nil:
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pull": p})
}
