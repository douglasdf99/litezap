package api

import (
	"net/http"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/go-chi/chi/v5"
)

// subscribePresence serves POST /instances/{id}/presence/subscribe {"to": phone or JID}.
func (s *server) subscribePresence(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To string `json:"to"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.To == "" {
		s.writeErr(w, apierr.Validation("to is required"))
		return
	}
	if err := s.msg.SubscribePresence(r.Context(), chi.URLParam(r, "id"), in.To); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
