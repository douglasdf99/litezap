package api

import (
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/settings"
	"github.com/go-chi/chi/v5"
)

const (
	// maxSendBody fits a 100MB document as base64 (spec 4.5) plus the JSON around it.
	maxSendBody = 140 << 20
	// bigBody is the size from which a send body takes one of the server's bodySlots.
	bigBody = 1 << 20
	// sendReadTimeout replaces the server's 30s ReadTimeout for send bodies: ~140MB over a
	// slow link takes minutes.
	sendReadTimeout = 5 * time.Minute
)

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.msg.Settings(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, s.settingsView(r, set))
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	var patch settings.Patch
	if err := decode(w, r, &patch); err != nil {
		s.writeErr(w, err)
		return
	}
	// The proxy decides the egress IP and unsafe_pacing lifts the anti-ban floors: admin
	// only, or operator only when locked (operator.go).
	if err := s.checkSettingsPatch(r, patch); err != nil {
		apierr.Write(w, err)
		return
	}
	set, err := s.msg.UpdateSettings(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, s.settingsView(r, set))
}

// sendMessage answers 202 while the message is queued and 200 once it was sent
// (?wait=true&timeout=30s waits for the outcome, spec 4.1).
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var wait time.Duration
	if q := r.URL.Query(); q.Get("wait") == "true" {
		wait = 30 * time.Second
		if t := q.Get("timeout"); t != "" {
			d, err := time.ParseDuration(t)
			if err != nil || d <= 0 {
				s.writeErr(w, apierr.Validation("timeout must be a duration such as 30s"))
				return
			}
			wait = d
		}
	}
	v, err := s.enqueue(w, r)
	if err == nil && wait > 0 && (v.Status == "queued" || v.Status == "in_flight") {
		v, err = s.msg.Wait(r.Context(), chi.URLParam(r, "id"), v.ID, wait)
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	code := http.StatusAccepted
	switch v.Status {
	case "sent", "delivered", "read":
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

// enqueue reads the body and queues the message. A large body holds one of bodySlots only
// until the message is queued: a ?wait=true request does not keep it (or its memory).
func (s *server) enqueue(w http.ResponseWriter, r *http.Request) (service.MessageView, error) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(sendReadTimeout))
	if r.ContentLength > bigBody || r.ContentLength < 0 {
		select { // large bodies are held in memory while decoded: a few at a time
		case s.bodySlots <- struct{}{}:
			defer func() { <-s.bodySlots }()
		case <-r.Context().Done():
			return service.MessageView{}, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many large sends in progress", true)
		}
	}
	var in service.SendInput
	if err := decodeMax(w, r, &in, maxSendBody); err != nil {
		return service.MessageView{}, err
	}
	return s.msg.Send(r.Context(), chi.URLParam(r, "id"), in, 0)
}

func (s *server) getMessage(w http.ResponseWriter, r *http.Request) {
	v, err := s.msg.GetMessage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "msg_id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func limitParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

func (s *server) listChats(w http.ResponseWriter, r *http.Request) {
	page, err := s.msg.Chats(r.Context(), chi.URLParam(r, "id"), limitParam(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) chatMessages(w http.ResponseWriter, r *http.Request) {
	page, err := s.msg.ChatMessages(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"),
		limitParam(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) markRead(w http.ResponseWriter, r *http.Request) {
	if err := s.msg.MarkRead(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listContacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.msg.Contacts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"contacts": list})
}

func (s *server) getContact(w http.ResponseWriter, r *http.Request) {
	c, err := s.msg.Contact(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *server) checkNumbers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phones []string `json:"phones"`
	}
	if err := decode(w, r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.msg.CheckNumbers(r.Context(), chi.URLParam(r, "id"), req.Phones)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

func (s *server) getMedia(w http.ResponseWriter, r *http.Request) {
	id, mediaID := chi.URLParam(r, "id"), chi.URLParam(r, "media_id")
	if r.URL.Query().Get("signed") == "1" {
		u, exp, err := s.msg.SignedMediaURL(r.Context(), id, mediaID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w)
		writeJSON(w, 200, map[string]any{"url": u, "expires_at": exp})
		return
	}
	s.serveMedia(w, r, id, mediaID)
}

func (s *server) signedMedia(w http.ResponseWriter, r *http.Request) {
	id, mediaID := chi.URLParam(r, "id"), chi.URLParam(r, "media_id")
	q := r.URL.Query()
	if !s.msg.VerifyMediaSignature(id, mediaID, q.Get("exp"), q.Get("sig")) {
		apierr.Write(w, apierr.New(http.StatusForbidden, "forbidden", "invalid or expired media URL", false))
		return
	}
	s.serveMedia(w, r, id, mediaID)
}

func (s *server) serveMedia(w http.ResponseWriter, r *http.Request, id, mediaID string) {
	mf, err := s.msg.Media(r.Context(), id, mediaID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	f, err := os.Open(mf.Path)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	if mf.Mime != "" {
		h.Set("Content-Type", mf.Mime)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	// Received media is attacker-controlled: never sniffed, never scripted, and anything
	// but plain images, audio and video is downloaded instead of rendered.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	disposition := "attachment"
	if inlineSafe(mf.Mime) {
		disposition = "inline"
	}
	params := map[string]string{}
	if mf.FileName != "" {
		params["filename"] = mf.FileName
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, params))
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// inlineSafe lists the types a browser may render in place (never SVG or HTML).
func inlineSafe(mimeType string) bool {
	m, _, _ := strings.Cut(strings.ToLower(mimeType), ";")
	switch strings.TrimSpace(m) {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return strings.HasPrefix(m, "audio/") || strings.HasPrefix(m, "video/")
}
