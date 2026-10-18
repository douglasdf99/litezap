package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// AccountService holds the marco 9 routes (chats, contacts, profile, privacy);
// service.Account implements it.
type AccountService interface {
	GetChat(ctx context.Context, instanceID, chat string) (service.ChatView, error)
	UpdateChat(ctx context.Context, instanceID, chat string, p service.ChatPatch) (service.ChatView, error)
	DeleteChat(ctx context.Context, instanceID, chat string, deleteMedia bool) error
	StarMessage(ctx context.Context, instanceID, id string, starred bool) (service.MessageView, error)
	ContactPicture(ctx context.Context, instanceID, jid string, preview bool) (service.PictureView, error)
	ContactPictureFile(ctx context.Context, instanceID, jid string, preview bool) (service.MediaFile, error)
	ContactProfile(ctx context.Context, instanceID, jid string) (service.ContactProfileView, error)
	Blocklist(ctx context.Context, instanceID string) ([]string, error)
	SetBlocked(ctx context.Context, instanceID, jid string, block bool) ([]string, error)
	Profile(ctx context.Context, instanceID string) (service.ProfileView, error)
	UpdateProfile(ctx context.Context, instanceID string, p service.ProfilePatch) (service.ProfileView, error)
	SetProfilePicture(ctx context.Context, instanceID string, in service.PictureInput) (service.ProfileView, error)
	RemoveProfilePicture(ctx context.Context, instanceID string) error
	Privacy(ctx context.Context, instanceID string) (service.PrivacyView, error)
	UpdatePrivacy(ctx context.Context, instanceID string, p service.PrivacyPatch) (service.PrivacyView, error)
}

// accountRoutes mounts the marco 9 routes inside /instances/{id}.
func (s *server) accountRoutes(r chi.Router) {
	if s.opt.Account == nil {
		return
	}
	r.Get("/chats/{jid}", s.getChat)
	r.Patch("/chats/{jid}", s.patchChat)
	r.Delete("/chats/{jid}", s.deleteChat)
	r.Put("/messages/{msg_id}/star", s.starMessage)
	r.Get("/contacts/{jid}/picture", s.contactPicture)
	r.Get("/contacts/{jid}/profile", s.contactProfile)
	r.Post("/contacts/{jid}/block", s.blockContact(true))
	r.Post("/contacts/{jid}/unblock", s.blockContact(false))
	r.Get("/blocklist", s.blocklist)
	r.Get("/profile", s.getProfile)
	r.Patch("/profile", s.patchProfile)
	r.Put("/profile/picture", s.putProfilePicture)
	r.Delete("/profile/picture", s.deleteProfilePicture)
	r.Get("/privacy", s.getPrivacy)
	r.Patch("/privacy", s.patchPrivacy)
}

// decodeStrict decodes a JSON body rejecting unknown fields (typos in a PATCH would
// otherwise silently change nothing).
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apierr.Validation("invalid JSON body: " + err.Error())
	}
	return nil
}

func (s *server) getChat(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Account.GetChat(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) patchChat(w http.ResponseWriter, r *http.Request) {
	var p service.ChatPatch
	if err := decodeStrict(w, r, &p); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Account.UpdateChat(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteChat(w http.ResponseWriter, r *http.Request) {
	media := r.URL.Query().Get("delete_media") == "1"
	if err := s.opt.Account.DeleteChat(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), media); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) starMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Starred *bool `json:"starred"`
	}
	if err := decodeStrict(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Starred == nil {
		s.writeErr(w, apierr.Validation("starred is required"))
		return
	}
	v, err := s.opt.Account.StarMessage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "msg_id"), *in.Starred)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) contactPicture(w http.ResponseWriter, r *http.Request) {
	id, jid := chi.URLParam(r, "id"), chi.URLParam(r, "jid")
	preview := r.URL.Query().Get("preview") == "1"
	if r.URL.Query().Get("download") == "1" {
		mf, err := s.opt.Account.ContactPictureFile(r.Context(), id, jid, preview)
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
		w.Header().Set("Content-Type", mf.Mime)
		w.Header().Set("Cache-Control", "private, max-age=600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "", time.Time{}, f)
		return
	}
	v, err := s.opt.Account.ContactPicture(r.Context(), id, jid, preview)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) contactProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Account.ContactProfile(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) blockContact(block bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.opt.Account.SetBlocked(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), block)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"blocked": list})
	}
}

func (s *server) blocklist(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Account.Blocklist(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"blocked": list})
}

func (s *server) getProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Account.Profile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) patchProfile(w http.ResponseWriter, r *http.Request) {
	var p service.ProfilePatch
	if err := decodeStrict(w, r, &p); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Account.UpdateProfile(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) putProfilePicture(w http.ResponseWriter, r *http.Request) {
	var in service.PictureInput
	// base64 of a 10MB picture is ~14MB.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 15<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		s.writeErr(w, apierr.Validation("invalid JSON body: "+err.Error()))
		return
	}
	v, err := s.opt.Account.SetProfilePicture(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteProfilePicture(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Account.RemoveProfilePicture(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getPrivacy(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Account.Privacy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// patchPrivacy takes {"last_seen": "contacts", ..., "default_disappearing_seconds": 86400}.
func (s *server) patchPrivacy(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := decode(w, r, &raw); err != nil {
		s.writeErr(w, err)
		return
	}
	p := service.PrivacyPatch{Settings: map[string]string{}}
	for k, v := range raw {
		if k == "default_disappearing_seconds" {
			var n int
			if err := json.Unmarshal(v, &n); err != nil {
				s.writeErr(w, apierr.Validation("default_disappearing_seconds must be a number"))
				return
			}
			p.DefaultDisappearingSeconds = &n
			continue
		}
		var str string
		if err := json.Unmarshal(v, &str); err != nil {
			s.writeErr(w, apierr.Validation(k+" must be a string"))
			return
		}
		p.Settings[k] = str
	}
	v, err := s.opt.Account.UpdatePrivacy(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
