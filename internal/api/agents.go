package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/douglasdf99/litezap/internal/agents"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/go-chi/chi/v5"
)

// AgentsService is the LiteZap agents administration (spec 2026-10-20); agents.Runner
// implements it.
type AgentsService interface {
	List(ctx context.Context, instanceID string) ([]agents.View, error)
	Get(ctx context.Context, instanceID, id string) (agents.View, error)
	Create(ctx context.Context, instanceID string, in agents.Input) (agents.View, error)
	Update(ctx context.Context, instanceID, id string, in agents.Input) (agents.View, error)
	Delete(ctx context.Context, instanceID, id string) error
	RunNow(ctx context.Context, instanceID, id string, in agents.RunNowInput) (agents.RunView, error)
	Runs(ctx context.Context, instanceID, agentID string, limit int) ([]agents.RunView, error)
	Run(ctx context.Context, instanceID, runID string) (agents.RunView, error)
	Drafts(ctx context.Context, instanceID, status string, limit int) ([]agents.DraftView, error)
	Decide(ctx context.Context, instanceID, draftID string, approve bool, decidedBy string) (agents.DraftView, error)
	Simulate(ctx context.Context, instanceID string, in agents.SimulateInput) (agents.RunView, error)
}

var _ AgentsService = (*agents.Runner)(nil)

// agentsRoutes mounts the agents routes inside /instances/{id} (instance token or admin;
// the AI provider itself is the attendant's, admin only).
func (s *server) agentsRoutes(r chi.Router) {
	if s.opt.Agents == nil {
		return
	}
	r.Get("/agents", s.listAgents)
	r.Post("/agents", s.createAgent)
	r.Post("/agents/simulate", s.simulateAgent)
	r.Get("/agents/drafts", s.listAgentDrafts)
	r.Post("/agents/drafts/{draft_id}/approve", s.decideAgentDraft(true))
	r.Post("/agents/drafts/{draft_id}/reject", s.decideAgentDraft(false))
	r.Get("/agents/runs/{run_id}", s.getAgentRun)
	r.Get("/agents/{agent_id}", s.getAgent)
	r.Put("/agents/{agent_id}", s.updateAgent)
	r.Delete("/agents/{agent_id}", s.deleteAgent)
	r.Post("/agents/{agent_id}/run", s.runAgent)
	r.Get("/agents/{agent_id}/runs", s.listAgentRuns)
}

const maxAgentBody = 256 << 10

func (s *server) listAgents(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Agents.List(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"agents": list, "groups": agents.Groups, "risky_groups": agents.RiskyGroups})
}

func (s *server) getAgent(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Agents.Get(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in agents.Input
	if err := decodeStrictMax(w, r, &in, maxAgentBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Agents.Create(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var in agents.Input
	if err := decodeStrictMax(w, r, &in, maxAgentBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Agents.Update(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Agents.Delete(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) runAgent(w http.ResponseWriter, r *http.Request) {
	var in agents.RunNowInput
	if r.ContentLength != 0 {
		if err := decodeStrictMax(w, r, &in, 64<<10); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	v, err := s.opt.Agents.RunNow(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func queryLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		return 0, apierr.Validation("limit must be between 1 and 200")
	}
	return n, nil
}

func (s *server) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	list, err := s.opt.Agents.Runs(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "agent_id"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"runs": list})
}

func (s *server) getAgentRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Agents.Run(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "run_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) listAgentDrafts(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	list, err := s.opt.Agents.Drafts(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("status"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"drafts": list})
}

func (s *server) decideAgentDraft(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		by := "instance"
		if p.admin {
			by = p.role()
		}
		v, err := s.opt.Agents.Decide(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "draft_id"), approve, by)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func (s *server) simulateAgent(w http.ResponseWriter, r *http.Request) {
	var in agents.SimulateInput
	if err := decodeStrictMax(w, r, &in, maxAgentBody); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Agents.Simulate(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
