package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// SocialService is status, channels, labels and the business profile (marco 10);
// service.Social implements it.
type SocialService interface {
	PostStatus(ctx context.Context, instanceID string, in service.StatusInput, wait time.Duration) (service.MessageView, error)
	DeleteStatus(ctx context.Context, instanceID, msgID string, wait time.Duration) (service.MessageView, error)
	ReplyStatus(ctx context.Context, instanceID, msgID string, in service.StatusReplyInput, wait time.Duration) (service.MessageView, error)
	StatusUpdates(ctx context.Context, instanceID string, limit int, cursor string) (service.MessagePage, error)
	StatusPrivacy(ctx context.Context, instanceID string) (service.StatusPrivacyView, error)

	Channels(ctx context.Context, instanceID string) ([]service.ChannelView, error)
	Channel(ctx context.Context, instanceID, jid, invite string) (service.ChannelView, error)
	CreateChannel(ctx context.Context, instanceID string, in service.CreateChannelInput) (service.ChannelView, error)
	ChannelAction(ctx context.Context, instanceID, jid, action string) error
	ChannelMessages(ctx context.Context, instanceID, jid string, count int, before int64) ([]service.ChannelPostView, error)
	ChannelReact(ctx context.Context, instanceID, jid string, serverID int64, emoji string) error
	PublishChannel(ctx context.Context, instanceID, jid string, in service.ChannelPostInput, wait time.Duration) (service.MessageView, error)

	Labels(ctx context.Context, instanceID string) ([]service.LabelView, error)
	CreateLabel(ctx context.Context, instanceID string, in service.LabelInput) (service.LabelView, error)
	UpdateLabel(ctx context.Context, instanceID, id string, in service.LabelInput) (service.LabelView, error)
	DeleteLabel(ctx context.Context, instanceID, id string) error
	LabelChat(ctx context.Context, instanceID, id, chat string, labeled bool) (service.LabelView, error)
	LabelMessage(ctx context.Context, instanceID, id, msgID string, labeled bool) error

	BusinessProfile(ctx context.Context, instanceID string) (service.BusinessAccountView, error)
	UpdateBusinessProfile(ctx context.Context, instanceID string, p service.BusinessProfilePatch) (service.BusinessAccountView, error)
}

// socialRoutes mounts the marco 10 routes inside /instances/{id}.
func (s *server) socialRoutes(r chi.Router) {
	if s.opt.Social == nil {
		return
	}
	r.Post("/status", s.postStatus)
	r.Get("/status", s.statusUpdates)
	r.Get("/status/privacy", s.statusPrivacy)
	r.Delete("/status/{msg_id}", s.deleteStatus)
	r.Post("/status/{msg_id}/reply", s.replyStatus)

	r.Get("/channels", s.listChannels)
	r.Post("/channels", s.createChannel)
	r.Post("/channels/preview", s.previewChannel)
	r.Get("/channels/{jid}", s.getChannel)
	r.Post("/channels/{jid}/follow", s.channelAction("follow"))
	r.Post("/channels/{jid}/unfollow", s.channelAction("unfollow"))
	r.Post("/channels/{jid}/mute", s.channelAction("mute"))
	r.Post("/channels/{jid}/unmute", s.channelAction("unmute"))
	r.Get("/channels/{jid}/messages", s.channelMessages)
	r.Post("/channels/{jid}/messages", s.publishChannel)
	r.Post("/channels/{jid}/reactions", s.channelReact)

	r.Get("/labels", s.listLabels)
	r.Post("/labels", s.createLabel)
	r.Patch("/labels/{label_id}", s.updateLabel)
	r.Delete("/labels/{label_id}", s.deleteLabel)
	r.Put("/labels/{label_id}/chats/{jid}", s.labelChat(true))
	r.Delete("/labels/{label_id}/chats/{jid}", s.labelChat(false))
	r.Put("/labels/{label_id}/messages/{msg_id}", s.labelMessage(true))
	r.Delete("/labels/{label_id}/messages/{msg_id}", s.labelMessage(false))

	r.Get("/business/profile", s.businessProfile)
	// A write: admin or instance token (scoped API keys never reach PATCH routes).
	r.Patch("/business/profile", s.patchBusinessProfile)
}

// waitParam reads ?wait=true&timeout=30s.
func waitParam(r *http.Request) (time.Duration, error) {
	q := r.URL.Query()
	if q.Get("wait") != "true" {
		return 0, nil
	}
	if t := q.Get("timeout"); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return 0, apierr.Validation("timeout must be a duration such as 30s")
		}
		return d, nil
	}
	return 30 * time.Second, nil
}

// queuedPost decodes a (possibly large) body into in, queues it with enqueue and answers
// like POST /messages: 200 once sent, 202 while queued (?wait=true waits after the body is
// released).
func queuedPost[T any](s *server, w http.ResponseWriter, r *http.Request, enqueue func(T) (service.MessageView, error)) {
	wait, err := waitParam(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := func() (service.MessageView, error) {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(sendReadTimeout))
		if r.ContentLength > bigBody || r.ContentLength < 0 {
			select {
			case s.bodySlots <- struct{}{}:
				defer func() { <-s.bodySlots }()
			case <-r.Context().Done():
				return service.MessageView{}, apierr.New(http.StatusServiceUnavailable, "rate_limited", "too many large sends in progress", true)
			}
		}
		var in T
		if err := decodeMax(w, r, &in, maxSendBody); err != nil {
			return service.MessageView{}, err
		}
		return enqueue(in)
	}()
	if err == nil && wait > 0 && (v.Status == "queued" || v.Status == "in_flight") {
		v, err = s.msg.Wait(r.Context(), chi.URLParam(r, "id"), v.ID, wait)
	}
	s.writeQueued(w, v, err)
}

func (s *server) writeQueued(w http.ResponseWriter, v service.MessageView, err error) {
	if err != nil {
		s.writeErr(w, err)
		return
	}
	code := http.StatusAccepted
	switch v.Status {
	case "sent", "delivered", "read", "played":
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

func (s *server) postStatus(w http.ResponseWriter, r *http.Request) {
	queuedPost(s, w, r, func(in service.StatusInput) (service.MessageView, error) {
		return s.opt.Social.PostStatus(r.Context(), chi.URLParam(r, "id"), in, 0)
	})
}

func (s *server) deleteStatus(w http.ResponseWriter, r *http.Request) {
	wait, err := waitParam(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.DeleteStatus(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "msg_id"), wait)
	s.writeQueued(w, v, err)
}

func (s *server) replyStatus(w http.ResponseWriter, r *http.Request) {
	wait, err := waitParam(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	var in service.StatusReplyInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.ReplyStatus(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "msg_id"), in, wait)
	s.writeQueued(w, v, err)
}

func (s *server) statusUpdates(w http.ResponseWriter, r *http.Request) {
	page, err := s.opt.Social.StatusUpdates(r.Context(), chi.URLParam(r, "id"), limitParam(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *server) statusPrivacy(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Social.StatusPrivacy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) listChannels(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Social.Channels(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"channels": list})
}

func (s *server) getChannel(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Social.Channel(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) previewChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Invite string `json:"invite"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Invite == "" {
		s.writeErr(w, apierr.Validation("invite is required"))
		return
	}
	v, err := s.opt.Social.Channel(r.Context(), chi.URLParam(r, "id"), "", in.Invite)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) createChannel(w http.ResponseWriter, r *http.Request) {
	var in service.CreateChannelInput
	if err := decodeMax(w, r, &in, 16<<20); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.CreateChannel(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) channelAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.opt.Social.ChannelAction(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), action); err != nil {
			s.writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) channelMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	count, before := 0, int64(0)
	var err error
	if v := q.Get("count"); v != "" {
		if count, err = strconv.Atoi(v); err != nil {
			s.writeErr(w, apierr.Validation("count must be a number"))
			return
		}
	}
	if v := q.Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil {
			s.writeErr(w, apierr.Validation("before must be a server id"))
			return
		}
	}
	posts, err := s.opt.Social.ChannelMessages(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), count, before)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": posts})
}

func (s *server) publishChannel(w http.ResponseWriter, r *http.Request) {
	queuedPost(s, w, r, func(in service.ChannelPostInput) (service.MessageView, error) {
		return s.opt.Social.PublishChannel(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), in, 0)
	})
}

func (s *server) channelReact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerID int64  `json:"server_id"`
		Emoji    string `json:"emoji"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Social.ChannelReact(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), in.ServerID, in.Emoji); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listLabels(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Social.Labels(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"labels": list})
}

func (s *server) createLabel(w http.ResponseWriter, r *http.Request) {
	var in service.LabelInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.CreateLabel(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) updateLabel(w http.ResponseWriter, r *http.Request) {
	var in service.LabelInput
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.UpdateLabel(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "label_id"), in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Social.DeleteLabel(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "label_id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) labelChat(labeled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.opt.Social.LabelChat(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "label_id"), chi.URLParam(r, "jid"), labeled)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func (s *server) labelMessage(labeled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.opt.Social.LabelMessage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "label_id"), chi.URLParam(r, "msg_id"), labeled); err != nil {
			s.writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) businessProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Social.BusinessProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) patchBusinessProfile(w http.ResponseWriter, r *http.Request) {
	var p service.BusinessProfilePatch
	if err := decodeStrict(w, r, &p); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Social.UpdateBusinessProfile(r.Context(), chi.URLParam(r, "id"), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
