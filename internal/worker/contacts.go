package worker

import (
	"context"
	"strings"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// Address book contacts (CON-2). WhatsApp keeps the account's contacts in the app state
// (collection critical_unblock_low, index ["contact", <phone JID>]); whatsmeow decodes
// them (events.Contact) but has no builder. Saving is a SET of a ContactAction, as
// WhatsApp Web's "new contact" sends it (version 2); removing is a REMOVE of the same
// index (appstate_remove.go).

// contactMutationVersion is the version WhatsApp Web (and Baileys) give contact mutations.
const contactMutationVersion = 2

const maxContactName = 128

func (s *Server) ContactAction(ctx context.Context, req *workerv1.ContactActionRequest) (*workerv1.ContactActionResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.ContactAction(ctx, req)
}

// contactMutation is the app state entry of a contact; value is nil for a removal.
func contactMutation(pn, lid types.JID, req *workerv1.ContactActionRequest) appstate.MutationInfo {
	act := &waSyncAction.ContactAction{}
	if req.Action == "save" {
		act.FirstName = proto.String(req.FirstName)
		act.FullName = proto.String(req.FullName)
		act.PnJID = proto.String(pn.String())
		act.SaveOnPrimaryAddressbook = proto.Bool(req.SaveOnPhone)
		if !lid.IsEmpty() {
			act.LidJID = proto.String(lid.String())
		}
	}
	return appstate.MutationInfo{
		Index:   []string{appstate.IndexContact, pn.String()},
		Version: contactMutationVersion,
		Value:   &waSyncAction.SyncActionValue{ContactAction: act},
	}
}

func (w *Whatsmeow) ContactAction(ctx context.Context, req *workerv1.ContactActionRequest) (*workerv1.ContactActionResponse, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	pn, err := parseUser(req.Jid)
	if err != nil {
		return nil, err
	}
	if pn.Server != types.DefaultUserServer {
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "contacts are saved by phone number")
	}
	lid := w.lidOf(ctx, pn)
	h := w.hooks()
	switch req.Action {
	case "save":
		req.FirstName, req.FullName = strings.TrimSpace(req.FirstName), strings.TrimSpace(req.FullName)
		if req.FullName == "" || len(req.FullName) > maxContactName || len(req.FirstName) > maxContactName {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "a contact needs a name of up to 128 bytes")
		}
		m := contactMutation(pn, lid, req)
		err = h.sendAppState(ctx, appstate.PatchInfo{Type: appstate.WAPatchCriticalUnblockLow, Mutations: []appstate.MutationInfo{m}})
	case "remove":
		err = h.removeAppState(ctx, appstate.WAPatchCriticalUnblockLow, contactMutation(pn, lid, req))
	default:
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "unknown contact action")
	}
	if err != nil {
		return nil, accountErr(err)
	}
	out := &workerv1.ContactActionResponse{Jid: pn.String()}
	if !lid.IsEmpty() {
		out.Lid = lid.String()
	}
	return out, nil
}
