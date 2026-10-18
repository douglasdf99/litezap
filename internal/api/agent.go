package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/douglasdf99/litezap/internal/agenthook"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/go-chi/chi/v5"
)

// AgentHookService is the agent hook administration (spec 4.3); agenthook.Admin
// implements it.
type AgentHookService interface {
	Get(ctx context.Context, instanceID string) (agenthook.View, error)
	Put(ctx context.Context, instanceID string, p agenthook.Patch) (agenthook.View, error)
	Delete(ctx context.Context, instanceID string) error
	SetChat(ctx context.Context, instanceID, chat string, enabled *bool) (agenthook.ChatView, error)
	Chats(ctx context.Context, instanceID string) ([]agenthook.ChatView, error)
	Calls(ctx context.Context, instanceID string, limit int) ([]agenthook.CallView, error)
}

// agentRoutes mounts the agent hook routes inside /instances/{id}.
func (s *server) agentRoutes(r chi.Router) {
	if s.opt.AgentHook == nil {
		return
	}
	r.Get("/agent-hook", s.getAgentHook)
	r.Put("/agent-hook", s.putAgentHook)
	r.Delete("/agent-hook", s.deleteAgentHook)
	r.Get("/agent-hook/chats", s.agentChats)
	r.Get("/agent-hook/calls", s.agentCalls)
	r.Put("/chats/{jid}/agent", s.putChatAgent)
}

func (s *server) getAgentHook(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.AgentHook.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) putAgentHook(w http.ResponseWriter, r *http.Request) {
	var p agenthook.Patch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		s.writeErr(w, apierr.Validation("invalid agent hook config: "+err.Error()))
		return
	}
	v, err := s.opt.AgentHook.Put(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if v.Secret != "" {
		noStore(w)
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteAgentHook(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.AgentHook.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) agentChats(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.AgentHook.Chats(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"chats": list})
}

func (s *server) agentCalls(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeErr(w, apierr.Validation("limit must be a positive integer"))
			return
		}
		limit = n
	}
	list, err := s.opt.AgentHook.Calls(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"calls": list})
}

// putChatAgent takes {"enabled": true | false | null}; the field is required (null
// means "inherit the instance default").
func (s *server) putChatAgent(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := decode(w, r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	raw, ok := body["enabled"]
	if !ok || len(body) != 1 {
		s.writeErr(w, apierr.Validation(`body must be {"enabled": true | false | null}`))
		return
	}
	var enabled *bool
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			s.writeErr(w, apierr.Validation("enabled must be true, false or null"))
			return
		}
		enabled = &b
	}
	v, err := s.opt.AgentHook.SetChat(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), enabled)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
