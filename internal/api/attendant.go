package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/attendant"
	"github.com/go-chi/chi/v5"
)

// AttendantService is the built-in attendant administration (spec 4.7); attendant.Admin
// implements it.
type AttendantService interface {
	GetSettings(ctx context.Context, id string) (attendant.SettingsView, error)
	PutSettings(ctx context.Context, id string, p attendant.SettingsPatch) (attendant.SettingsView, error)
	Presets() map[string]attendant.Profile
	Usage(ctx context.Context, id string, days int) ([]attendant.UsageView, error)
	ListAgents(ctx context.Context, id string) ([]attendant.AgentView, error)
	GetAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error)
	CreateAgent(ctx context.Context, id string, p attendant.Profile) (attendant.AgentView, error)
	UpdateAgent(ctx context.Context, id, agentID string, p attendant.Profile) (attendant.AgentView, error)
	DeleteAgent(ctx context.Context, id, agentID string) error
	ActivateAgent(ctx context.Context, id, agentID string) (attendant.AgentView, error)
	Simulate(ctx context.Context, id string, in attendant.SimulateInput) (attendant.SimulateResult, error)
}

// attendantRoutes mounts the attendant routes inside /instances/{id}. The provider and its
// keys are admin-only; agents and simulations work with the instance token.
func (s *server) attendantRoutes(r chi.Router) {
	if s.opt.Attendant == nil {
		return
	}
	r.Get("/attendant", s.getAttendant)
	r.With(s.requireAdmin).Put("/attendant", s.putAttendant)
	r.Get("/attendant/presets", s.attendantPresets)
	r.Get("/attendant/usage", s.attendantUsage)
	r.Get("/attendant/agents", s.listAttendantAgents)
	r.Post("/attendant/agents", s.createAttendantAgent)
	r.Get("/attendant/agents/{agent_id}", s.getAttendantAgent)
	r.Put("/attendant/agents/{agent_id}", s.updateAttendantAgent)
	r.Delete("/attendant/agents/{agent_id}", s.deleteAttendantAgent)
	r.Post("/attendant/agents/{agent_id}/activate", s.activateAttendantAgent)
	r.Post("/attendant/simulate", s.simulateAttendant)
}

// decodeStrictMax decodes a JSON body refusing unknown fields (typos in a profile would
// otherwise be silently dropped).
func decodeStrictMax(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apierr.Validation("invalid JSON body: " + err.Error())
	}
	return nil
}

func (s *server) getAttendant(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Attendant.GetSettings(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) putAttendant(w http.ResponseWriter, r *http.Request) {
	var p attendant.SettingsPatch
	if err := decodeStrictMax(w, r, &p, 64<<10); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Attendant.PutSettings(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noStore(w)
	writeJSON(w, 200, v)
}

func (s *server) attendantPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"presets": s.opt.Attendant.Presets()})
}

func (s *server) attendantUsage(w http.ResponseWriter, r *http.Request) {
	days := 0
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeErr(w, apierr.Validation("days must be a positive integer"))
			return
		}
		days = n
	}
	list, err := s.opt.Attendant.Usage(r.Context(), chi.URLParam(r, "id"), days)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"usage": list})
}

func (s *server) listAttendantAgents(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Attendant.ListAgents(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"agents": list})
}

func (s *server) getAttendantAgent(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Attendant.GetAgent(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

const maxProfileBody = 512 << 10

func (s *server) createAttendantAgent(w http.ResponseWriter, r *http.Request) {
	var p attendant.Profile
	if err := decodeStrictMax(w, r, &p, maxProfileBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Attendant.CreateAgent(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) updateAttendantAgent(w http.ResponseWriter, r *http.Request) {
	var p attendant.Profile
	if err := decodeStrictMax(w, r, &p, maxProfileBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Attendant.UpdateAgent(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteAttendantAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Attendant.DeleteAgent(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) activateAttendantAgent(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Attendant.ActivateAgent(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) simulateAttendant(w http.ResponseWriter, r *http.Request) {
	var in attendant.SimulateInput
	if err := decodeStrictMax(w, r, &in, maxProfileBody); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.opt.Attendant.Simulate(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}
