package api

import (
	"context"
	"net/http"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/go-chi/chi/v5"
)

// GroupService manages groups and communities (marco 8); service.Groups implements it.
type GroupService interface {
	List(ctx context.Context, instanceID string, communities bool) ([]service.GroupView, error)
	Get(ctx context.Context, instanceID, group string) (service.GroupView, error)
	Create(ctx context.Context, instanceID string, in service.CreateGroupInput) (service.CreateGroupResult, error)
	Update(ctx context.Context, instanceID, group string, in service.UpdateGroupInput) (service.GroupView, error)
	SetPhoto(ctx context.Context, instanceID, group string, in *service.PhotoInput) error
	Participants(ctx context.Context, instanceID, group, action string, participants []string) ([]service.ParticipantResultView, error)
	JoinRequests(ctx context.Context, instanceID, group string) ([]service.JoinRequestView, error)
	DecideJoinRequests(ctx context.Context, instanceID, group string, approve bool, participants []string) ([]service.ParticipantResultView, error)
	InviteLink(ctx context.Context, instanceID, group string, reset bool) (string, error)
	PreviewInvite(ctx context.Context, instanceID, code string) (service.GroupView, error)
	JoinInvite(ctx context.Context, instanceID, code string) (service.JoinResult, error)
	Leave(ctx context.Context, instanceID, group string) error
	Link(ctx context.Context, instanceID, community, group string, unlink bool) error
	SubGroups(ctx context.Context, instanceID, community string) ([]service.SubGroupView, error)
}

// groupRoutes mounts the group and community routes inside /instances/{id}. Instance
// tokens manage their own instance's groups (requireInstance); the anti-ban quotas
// apply to every caller.
func (s *server) groupRoutes(r chi.Router) {
	if s.opt.Groups == nil {
		return
	}
	r.Get("/groups", s.listGroups(false))
	r.Post("/groups", s.createGroup(false))
	r.Post("/groups/preview", s.previewInvite)
	r.Post("/groups/join", s.joinInvite)
	r.Get("/groups/{jid}", s.getGroup)
	r.Patch("/groups/{jid}", s.updateGroup)
	r.Put("/groups/{jid}/photo", s.setGroupPhoto)
	r.Delete("/groups/{jid}/photo", s.deleteGroupPhoto)
	r.Post("/groups/{jid}/participants", s.groupParticipants)
	r.Get("/groups/{jid}/requests", s.joinRequests)
	r.Post("/groups/{jid}/requests", s.decideJoinRequests)
	r.Get("/groups/{jid}/invite", s.inviteLink(false))
	r.Post("/groups/{jid}/invite/reset", s.inviteLink(true))
	r.Post("/groups/{jid}/leave", s.leaveGroup)

	r.Get("/communities", s.listGroups(true))
	r.Post("/communities", s.createGroup(true))
	r.Get("/communities/{jid}", s.getCommunity)
	r.Get("/communities/{jid}/groups", s.subGroups)
	r.Post("/communities/{jid}/groups", s.linkGroup)
	r.Delete("/communities/{jid}/groups/{group}", s.unlinkGroup)
}

func (s *server) listGroups(communities bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.opt.Groups.List(r.Context(), chi.URLParam(r, "id"), communities)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		key := "groups"
		if communities {
			key = "communities"
		}
		writeJSON(w, 200, map[string]any{key: list})
	}
}

func (s *server) createGroup(community bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in service.CreateGroupInput
		if err := decode(w, r, &in); err != nil {
			s.writeErr(w, err)
			return
		}
		in.Community = community
		if community && in.Parent != "" {
			s.writeErr(w, apierr.Validation("a community cannot have a parent"))
			return
		}
		res, err := s.opt.Groups.Create(r.Context(), chi.URLParam(r, "id"), in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	}
}

func (s *server) getGroup(w http.ResponseWriter, r *http.Request) {
	v, err := s.opt.Groups.Get(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) updateGroup(w http.ResponseWriter, r *http.Request) {
	// Settings may come flat ({"locked": true}) or nested like GroupView ({"settings": {...}}).
	var in struct {
		service.UpdateGroupInput
		Settings *service.GroupSettingsInput `json:"settings,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	up := in.UpdateGroupInput
	if st := in.Settings; st != nil {
		pickB := func(a, b *bool) *bool {
			if b != nil {
				return b
			}
			return a
		}
		up.Announce, up.Locked = pickB(up.Announce, st.Announce), pickB(up.Locked, st.Locked)
		up.ApprovalRequired, up.AdminsAddMembers = pickB(up.ApprovalRequired, st.ApprovalRequired), pickB(up.AdminsAddMembers, st.AdminsAddMembers)
		if st.EphemeralSeconds != nil {
			up.EphemeralSeconds = st.EphemeralSeconds
		}
	}
	v, err := s.opt.Groups.Update(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), up)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) setGroupPhoto(w http.ResponseWriter, r *http.Request) {
	var in service.PhotoInput
	if err := decodeMax(w, r, &in, 4<<20); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.opt.Groups.SetPhoto(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), &in); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) deleteGroupPhoto(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Groups.SetPhoto(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), nil); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type participantsBody struct {
	Action       string   `json:"action"`
	Participants []string `json:"participants"`
}

func (s *server) groupParticipants(w http.ResponseWriter, r *http.Request) {
	var in participantsBody
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.opt.Groups.Participants(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), in.Action, in.Participants)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

func (s *server) joinRequests(w http.ResponseWriter, r *http.Request) {
	list, err := s.opt.Groups.JoinRequests(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"requests": list})
}

func (s *server) decideJoinRequests(w http.ResponseWriter, r *http.Request) {
	var in participantsBody
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	var approve bool
	switch in.Action {
	case "approve":
		approve = true
	case "reject":
	default:
		s.writeErr(w, apierr.Validation("action must be approve or reject"))
		return
	}
	res, err := s.opt.Groups.DecideJoinRequests(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), approve, in.Participants)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": res})
}

func (s *server) inviteLink(reset bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		link, err := s.opt.Groups.InviteLink(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), reset)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		noStore(w) // whoever has the link can join
		writeJSON(w, 200, map[string]string{"link": link})
	}
}

type inviteBody struct {
	Code string `json:"code"`
}

func (s *server) previewInvite(w http.ResponseWriter, r *http.Request) {
	var in inviteBody
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	v, err := s.opt.Groups.PreviewInvite(r.Context(), chi.URLParam(r, "id"), in.Code)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) joinInvite(w http.ResponseWriter, r *http.Request) {
	var in inviteBody
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	res, err := s.opt.Groups.JoinInvite(r.Context(), chi.URLParam(r, "id"), in.Code)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) leaveGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Groups.Leave(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getCommunity(w http.ResponseWriter, r *http.Request) {
	id, jid := chi.URLParam(r, "id"), chi.URLParam(r, "jid")
	v, err := s.opt.Groups.Get(r.Context(), id, jid)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !v.Community {
		s.writeErr(w, apierr.New(http.StatusNotFound, "group_not_found", "not a community (see /groups/{jid})", false))
		return
	}
	subs, err := s.opt.Groups.SubGroups(r.Context(), id, jid)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"community": v, "groups": subs})
}

func (s *server) subGroups(w http.ResponseWriter, r *http.Request) {
	subs, err := s.opt.Groups.SubGroups(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"groups": subs})
}

func (s *server) linkGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Group string `json:"group"`
	}
	if err := decode(w, r, &in); err != nil {
		s.writeErr(w, err)
		return
	}
	if in.Group == "" {
		s.writeErr(w, apierr.Validation("group is required"))
		return
	}
	if err := s.opt.Groups.Link(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), in.Group, false); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) unlinkGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Groups.Link(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "jid"), chi.URLParam(r, "group"), true); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
