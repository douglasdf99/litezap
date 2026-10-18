package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// WebhookService is the subset of service.Webhooks used by the API.
type WebhookService interface {
	Create(ctx context.Context, sc service.Scope, in service.WebhookInput) (service.WebhookView, error)
	List(ctx context.Context, sc service.Scope, instanceID string) ([]service.WebhookView, error)
	Get(ctx context.Context, sc service.Scope, id string) (service.WebhookView, error)
	Update(ctx context.Context, sc service.Scope, id string, in service.WebhookInput) (service.WebhookView, error)
	Delete(ctx context.Context, sc service.Scope, id string) error
	Deliveries(ctx context.Context, sc service.Scope, id, status string, limit int, cursor string) (service.DeliveryPage, error)
	Retry(ctx context.Context, sc service.Scope, id, deliveryID string) (service.DeliveryView, error)
}

func scopeOf(r *http.Request) service.Scope {
	return principalFrom(r).scope()
}

func (s *server) webhookRoutes(r chi.Router) {
	wh := s.opt.Webhooks
	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		var in service.WebhookInput
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
		v, err := wh.Create(r.Context(), scopeOf(r), in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w) // carries the secret
		writeJSON(w, http.StatusCreated, v)
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		list, err := wh.List(r.Context(), scopeOf(r), r.URL.Query().Get("instance_id"))
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"webhooks": list})
	})
	r.Get("/{wid}", func(w http.ResponseWriter, r *http.Request) {
		v, err := wh.Get(r.Context(), scopeOf(r), chi.URLParam(r, "wid"))
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	r.Patch("/{wid}", func(w http.ResponseWriter, r *http.Request) {
		var in service.WebhookInput
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
		v, err := wh.Update(r.Context(), scopeOf(r), chi.URLParam(r, "wid"), in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w)
		writeJSON(w, 200, v)
	})
	r.Delete("/{wid}", func(w http.ResponseWriter, r *http.Request) {
		if err := wh.Delete(r.Context(), scopeOf(r), chi.URLParam(r, "wid")); err != nil {
			s.writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/{wid}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := wh.Deliveries(r.Context(), scopeOf(r), chi.URLParam(r, "wid"), q.Get("status"), limitParam(r), q.Get("cursor"))
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, page)
	})
	r.Post("/{wid}/deliveries/{did}/retry", func(w http.ResponseWriter, r *http.Request) {
		v, err := wh.Retry(r.Context(), scopeOf(r), chi.URLParam(r, "wid"), chi.URLParam(r, "did"))
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
}

// typeFilter parses ?types=message.*,instance.status (empty: every event).
func typeFilter(r *http.Request) ([]string, error) {
	raw := r.URL.Query().Get("types")
	if raw == "" {
		return nil, nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if !events.ValidPattern(p) {
			return nil, apierr.Validation("unknown event type filter: " + p)
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *server) allEvents(w http.ResponseWriter, r *http.Request) {
	types, err := typeFilter(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.stream(w, r, events.Filter{Types: types})
}

func (s *server) instanceEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	types, err := typeFilter(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if _, err := s.svc.Get(r.Context(), id); err != nil {
		s.writeErr(w, err)
		return
	}
	s.stream(w, r, events.Filter{InstanceID: id, Types: types})
}

const sseWriteTimeout = 10 * time.Second

// stream serves Server-Sent Events until the client leaves, the subscription is dropped
// (client too slow) or the server shuts down. The server has no WriteTimeout, so every
// write gets its own deadline through http.ResponseController.
func (s *server) stream(w http.ResponseWriter, r *http.Request, f events.Filter) {
	rc := http.NewResponseController(w)
	sub := s.opt.Events.Subscribe(f, s.opt.SSEBuffer)
	defer sub.Close()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // no proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	write := func(chunks ...string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c)); err != nil {
				return false
			}
		}
		return rc.Flush() == nil
	}
	if !write("retry: 3000\n: connected\n\n") {
		return
	}
	hb := time.NewTicker(s.opt.SSEHeartbeat)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub.C():
			if !ok {
				if sub.Dropped() {
					write("event: error\ndata: {\"code\":\"slow_consumer\",\"message\":\"stream too slow; reconnect\"}\n\n")
				}
				return
			}
			if !write("id: ", e.ID, "\nevent: ", e.Type, "\ndata: ", string(e.JSON), "\n\n") {
				return
			}
		case <-hb.C:
			if !write(": ping\n\n") {
				return
			}
		}
	}
}
