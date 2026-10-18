package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Account is implemented by WAClients that manage chats, contacts, the profile and the
// privacy settings (marco 9). Separate from Messenger so other clients keep compiling;
// without it the RPCs answer Unimplemented. Errors follow worker.proto; NotFound
// ("not_found: ...") means no profile picture and FailedPrecondition with
// "app_state_not_synced" means the app state keys have not arrived yet.
type Account interface {
	ChatAction(ctx context.Context, req *workerv1.ChatActionRequest) error
	ChatSettings(ctx context.Context, chat string) (*workerv1.GetChatSettingsResponse, error)
	ProfilePicture(ctx context.Context, jid string, preview bool) (*workerv1.GetProfilePictureResponse, error)
	DownloadProfilePicture(ctx context.Context, jid string, preview bool, dest string) (*workerv1.DownloadProfilePictureResponse, error)
	UserInfo(ctx context.Context, jid string, business bool) (*workerv1.GetUserInfoResponse, error)
	Blocklist(ctx context.Context) ([]string, error)
	UpdateBlocklist(ctx context.Context, jid string, block bool) ([]string, error)
	Profile(ctx context.Context) (*workerv1.GetProfileResponse, error)
	UpdateProfile(ctx context.Context, req *workerv1.UpdateProfileRequest) (*workerv1.UpdateProfileResponse, error)
	Privacy(ctx context.Context) (map[string]string, error)
	SetPrivacy(ctx context.Context, req *workerv1.SetPrivacyRequest) (map[string]string, error)
}

var _ Account = (*Whatsmeow)(nil)

func (s *Server) account() (Account, error) {
	a, ok := s.wa.(Account)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "chats, contacts and profile not supported by this worker")
	}
	return a, nil
}

func (s *Server) ChatAction(ctx context.Context, req *workerv1.ChatActionRequest) (*workerv1.ChatActionResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	if err := a.ChatAction(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.ChatActionResponse{}, nil
}

func (s *Server) GetChatSettings(ctx context.Context, req *workerv1.GetChatSettingsRequest) (*workerv1.GetChatSettingsResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.ChatSettings(ctx, req.Chat)
}

func (s *Server) GetProfilePicture(ctx context.Context, req *workerv1.GetProfilePictureRequest) (*workerv1.GetProfilePictureResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.ProfilePicture(ctx, req.Jid, req.Preview)
}

func (s *Server) DownloadProfilePicture(ctx context.Context, req *workerv1.DownloadProfilePictureRequest) (*workerv1.DownloadProfilePictureResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.DownloadProfilePicture(ctx, req.Jid, req.Preview, req.DestPath)
}

func (s *Server) GetUserInfo(ctx context.Context, req *workerv1.GetUserInfoRequest) (*workerv1.GetUserInfoResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.UserInfo(ctx, req.Jid, req.Business)
}

func (s *Server) GetBlocklist(ctx context.Context, _ *workerv1.GetBlocklistRequest) (*workerv1.GetBlocklistResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	jids, err := a.Blocklist(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetBlocklistResponse{Jids: jids}, nil
}

func (s *Server) UpdateBlocklist(ctx context.Context, req *workerv1.UpdateBlocklistRequest) (*workerv1.GetBlocklistResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	jids, err := a.UpdateBlocklist(ctx, req.Jid, req.Block)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetBlocklistResponse{Jids: jids}, nil
}

func (s *Server) GetProfile(ctx context.Context, _ *workerv1.GetProfileRequest) (*workerv1.GetProfileResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.Profile(ctx)
}

func (s *Server) UpdateProfile(ctx context.Context, req *workerv1.UpdateProfileRequest) (*workerv1.UpdateProfileResponse, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	return a.UpdateProfile(ctx, req)
}

func (s *Server) GetPrivacy(ctx context.Context, _ *workerv1.GetPrivacyRequest) (*workerv1.Privacy, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	m, err := a.Privacy(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.Privacy{Settings: m}, nil
}

func (s *Server) SetPrivacy(ctx context.Context, req *workerv1.SetPrivacyRequest) (*workerv1.Privacy, error) {
	a, err := s.account()
	if err != nil {
		return nil, err
	}
	m, err := a.SetPrivacy(ctx, req)
	if err != nil {
		return nil, err
	}
	return &workerv1.Privacy{Settings: m}, nil
}

// accountHooks are the whatsmeow calls used by the account methods; nil fields default to
// the client (tests replace them).
type accountHooks struct {
	// proxyURL is the instance proxy: profile pictures are downloaded through it, like media.
	proxyURL string

	sendAppState    func(context.Context, appstate.PatchInfo) error
	chatSettings    func(context.Context, types.JID) (types.LocalChatSettings, error)
	pictureInfo     func(context.Context, types.JID, *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)
	userInfo        func(context.Context, []types.JID) (map[types.JID]types.UserInfo, error)
	businessProfile func(context.Context, types.JID) (*types.BusinessProfile, error)
	getBlocklist    func(context.Context, string) (*types.Blocklist, error)
	updateBlocklist func(context.Context, types.JID, events.BlocklistChangeAction, string) (*types.Blocklist, error)
	setStatus       func(context.Context, types.SetStatusInput) error
	setPhoto        func(context.Context, types.JID, []byte) (string, error)
	privacy         func(context.Context, bool) (*types.PrivacySettings, error)
	setPrivacy      func(context.Context, types.PrivacySettingType, types.PrivacySetting) (types.PrivacySettings, error)
	setDisappearing func(context.Context, time.Duration) error
	httpClient      *http.Client
}

func (w *Whatsmeow) hooks() accountHooks {
	h := w.acct
	if h.sendAppState == nil {
		h.sendAppState = w.cli.SendAppState
	}
	if h.chatSettings == nil {
		h.chatSettings = func(ctx context.Context, jid types.JID) (types.LocalChatSettings, error) {
			if w.cli.Store == nil || w.cli.Store.ChatSettings == nil {
				return types.LocalChatSettings{}, nil
			}
			return w.cli.Store.ChatSettings.GetChatSettings(ctx, jid)
		}
	}
	if h.pictureInfo == nil {
		h.pictureInfo = w.cli.GetProfilePictureInfo
	}
	if h.userInfo == nil {
		h.userInfo = w.cli.GetUserInfo
	}
	if h.businessProfile == nil {
		h.businessProfile = w.cli.GetBusinessProfile
	}
	if h.getBlocklist == nil {
		h.getBlocklist = w.cli.GetBlocklist
	}
	if h.updateBlocklist == nil {
		h.updateBlocklist = w.cli.UpdateBlocklist
	}
	if h.setStatus == nil {
		h.setStatus = w.cli.SetStatusMessage
	}
	if h.setPhoto == nil {
		h.setPhoto = w.cli.SetGroupPhoto
	}
	if h.privacy == nil {
		h.privacy = w.cli.TryFetchPrivacySettings
	}
	if h.setPrivacy == nil {
		h.setPrivacy = w.cli.SetPrivacySetting
	}
	if h.setDisappearing == nil {
		h.setDisappearing = w.cli.SetDefaultDisappearingTimer
	}
	if h.httpClient == nil {
		// No keep-alive: a picture download is rare, and a client is built per call.
		tr := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 20 * time.Second, DisableKeepAlives: true}
		if u, err := url.Parse(h.proxyURL); err == nil && h.proxyURL != "" {
			tr.Proxy = http.ProxyURL(u) // http(s) and socks5, as for the websocket
		}
		h.httpClient = &http.Client{Transport: tr, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return h
}

// accountErr maps whatsmeow failures of the account calls onto worker.proto codes.
func accountErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return grpcErr(codes.NotFound, "not_found", "no profile picture")
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return grpcErr(codes.NotFound, "not_found", "profile picture hidden by the contact's privacy settings")
	case errors.Is(err, whatsmeow.ErrInvalidImageFormat):
		return grpcErr(codes.InvalidArgument, "validation_failed", "WhatsApp rejected the image (use a JPEG)")
	case errors.Is(err, whatsmeow.ErrIQNotAuthorized), errors.Is(err, whatsmeow.ErrIQForbidden):
		return grpcErr(codes.PermissionDenied, "forbidden", "WhatsApp refused the request")
	case err != nil && strings.Contains(err.Error(), "no app state keys found"):
		return grpcErr(codes.FailedPrecondition, "app_state_not_synced",
			"chat state keys not synced from the phone yet; keep the phone online and try again shortly")
	}
	return sendErr(err)
}

// loggedIn fails fast before the account calls: while pairing the socket is up but
// WhatsApp never answers these queries (they would hang until the RPC deadline).
func (w *Whatsmeow) loggedIn() error {
	if w.cli == nil || w.cli.Store == nil || w.cli.Store.ID == nil {
		return grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	}
	return nil
}

// parseUser parses a contact JID (phone-number or LID user, no device).
func parseUser(raw string) (types.JID, error) {
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "a contact JID is required")
	}
	return jid.ToNonAD(), nil
}

// parseChat parses a direct or group chat JID.
func parseChat(raw string) (types.JID, error) {
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "invalid chat JID")
	}
	switch jid.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return jid.ToNonAD(), nil
	}
	return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "chat actions apply to direct and group chats")
}

// lidOf returns the LID of a phone-number JID, or EmptyJID when unknown.
func (w *Whatsmeow) lidOf(ctx context.Context, pn types.JID) types.JID {
	lidFor := w.lidFor
	if lidFor == nil {
		if w.cli.Store == nil || w.cli.Store.LIDs == nil {
			return types.EmptyJID
		}
		lidFor = w.cli.Store.LIDs.GetLIDForPN
	}
	lid, err := lidFor(ctx, pn.ToNonAD())
	if err != nil || lid.IsEmpty() {
		return types.EmptyJID
	}
	return lid.ToNonAD()
}

// appStateJID is the JID a direct chat is indexed by in the app state. Accounts migrated
// to LIDs index direct chats by LID, so the LID is used when it is known, unless the
// local chat settings only know the phone-number form (an older chat).
func (w *Whatsmeow) appStateJID(ctx context.Context, chat types.JID) types.JID {
	if chat.Server != types.DefaultUserServer {
		return chat
	}
	if _, own := w.ownJIDs(); own.IsEmpty() {
		return chat
	}
	lid := w.lidOf(ctx, chat)
	if lid.IsEmpty() {
		return chat
	}
	h := w.hooks()
	pnSet, err1 := h.chatSettings(ctx, chat)
	lidSet, err2 := h.chatSettings(ctx, lid)
	if err1 == nil && err2 == nil && pnSet.Found && !lidSet.Found {
		return chat
	}
	return lid
}

// messageKey builds the key of the chat's last (or target) message.
func messageKey(target types.JID, ref *workerv1.MessageRef) (time.Time, *waCommon.MessageKey) {
	if ref == nil || ref.Id == "" {
		return time.Time{}, nil
	}
	var ts time.Time
	if ref.TimestampMs > 0 {
		ts = time.UnixMilli(ref.TimestampMs)
	}
	key := &waCommon.MessageKey{RemoteJID: proto.String(target.String()), FromMe: proto.Bool(ref.FromMe), ID: proto.String(ref.Id)}
	if target.Server == types.GroupServer && !ref.FromMe && ref.Sender != "" {
		key.Participant = proto.String(ref.Sender)
	}
	return ts, key
}

func (w *Whatsmeow) ChatAction(ctx context.Context, req *workerv1.ChatActionRequest) error {
	if err := w.loggedIn(); err != nil {
		return err
	}
	chat, err := parseChat(req.Chat)
	if err != nil {
		return err
	}
	target := w.appStateJID(ctx, chat)
	ts, key := messageKey(target, req.Message)
	var patch appstate.PatchInfo
	switch req.Action {
	case "archive", "unarchive":
		patch = appstate.BuildArchive(target, req.Action == "archive", ts, key)
	case "pin", "unpin":
		patch = appstate.BuildPin(target, req.Action == "pin")
	case "mute":
		var until *int64
		if req.MuteUntilMs > 0 {
			until = proto.Int64(req.MuteUntilMs)
		}
		patch = appstate.BuildMuteAbs(target, true, until)
	case "unmute":
		patch = appstate.BuildMuteAbs(target, false, nil)
	case "read", "unread":
		patch = appstate.BuildMarkChatAsRead(target, req.Action == "read", ts, key)
	case "delete":
		patch = appstate.BuildDeleteChat(target, ts, key, req.DeleteMedia)
	case "star", "unstar":
		m := req.Message
		if m == nil || m.Id == "" {
			return grpcErr(codes.InvalidArgument, "validation_failed", "star needs the message")
		}
		// Our own messages (and the contact's, in direct chats) are indexed with sender "0":
		// BuildStar writes "0" when sender and target are the same user.
		sender := target
		if !m.FromMe && target.Server == types.GroupServer {
			s, err := types.ParseJID(m.Sender)
			if err != nil || s.User == "" {
				return grpcErr(codes.InvalidArgument, "validation_failed", "star in a group needs the sender")
			}
			sender = w.groupParticipant(ctx, target, s.ToNonAD())
		}
		patch = appstate.BuildStar(target, sender, types.MessageID(m.Id), m.FromMe, req.Action == "star")
	default:
		return grpcErr(codes.InvalidArgument, "validation_failed", "unknown chat action")
	}
	if err := w.hooks().sendAppState(ctx, patch); err != nil {
		return accountErr(err)
	}
	return nil
}

func mutedUntilMs(t time.Time) int64 {
	switch {
	case t.IsZero():
		return 0
	case !t.Before(store.MutedForever):
		return -1
	}
	return t.UnixMilli()
}

func (w *Whatsmeow) ChatSettings(ctx context.Context, raw string) (*workerv1.GetChatSettingsResponse, error) {
	chat, err := parseChat(raw)
	if err != nil {
		return nil, err
	}
	h := w.hooks()
	forms := []types.JID{chat}
	if lid := w.lidOf(ctx, chat); chat.Server == types.DefaultUserServer && !lid.IsEmpty() {
		forms = []types.JID{lid, chat}
	}
	for _, j := range forms {
		cs, err := h.chatSettings(ctx, j)
		if err != nil {
			return nil, grpcErr(codes.Unknown, "worker_error", "cannot read chat settings")
		}
		if cs.Found {
			out := &workerv1.GetChatSettingsResponse{Found: true, Archived: cs.Archived, Pinned: cs.Pinned}
			if !cs.MutedUntil.IsZero() && (cs.MutedUntil.After(time.Now()) || !cs.MutedUntil.Before(store.MutedForever)) {
				out.MutedUntilMs = mutedUntilMs(cs.MutedUntil)
			}
			return out, nil
		}
	}
	return &workerv1.GetChatSettingsResponse{}, nil
}

// pictureTarget parses the JID of a picture lookup: a contact, a group, or "" (ourselves).
func (w *Whatsmeow) pictureTarget(raw string) (types.JID, error) {
	if raw == "" {
		pn, _ := w.ownJIDs()
		if pn.IsEmpty() {
			return types.EmptyJID, grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
		}
		return pn, nil
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" {
		return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "invalid JID")
	}
	switch jid.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return jid.ToNonAD(), nil
	}
	return types.EmptyJID, grpcErr(codes.InvalidArgument, "validation_failed", "pictures exist for contacts and groups")
}

func (w *Whatsmeow) pictureInfo(ctx context.Context, raw string, preview bool) (*types.ProfilePictureInfo, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	jid, err := w.pictureTarget(raw)
	if err != nil {
		return nil, err
	}
	info, err := w.hooks().pictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: preview})
	if err != nil {
		return nil, accountErr(err)
	}
	if info == nil || info.URL == "" {
		return nil, grpcErr(codes.NotFound, "not_found", "no profile picture")
	}
	return info, nil
}

func (w *Whatsmeow) ProfilePicture(ctx context.Context, jid string, preview bool) (*workerv1.GetProfilePictureResponse, error) {
	info, err := w.pictureInfo(ctx, jid, preview)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetProfilePictureResponse{Id: info.ID, Url: info.URL, Type: info.Type}, nil
}

// maxPictureBytes bounds a profile picture download (WhatsApp serves ~640x640 JPEGs).
const maxPictureBytes = 5 << 20

// pictureURL accepts only https URLs of WhatsApp's CDN (the URL comes from WhatsApp, but
// the worker must never be turned into a fetcher of arbitrary addresses).
func pictureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "whatsapp.net" || strings.HasSuffix(h, ".whatsapp.net")
}

func (w *Whatsmeow) DownloadProfilePicture(ctx context.Context, jid string, preview bool, dest string) (*workerv1.DownloadProfilePictureResponse, error) {
	path, err := w.dataPath(dest, "media")
	if err != nil {
		return nil, err
	}
	info, err := w.pictureInfo(ctx, jid, preview)
	if err != nil {
		return nil, err
	}
	if !pictureURL(info.URL) {
		return nil, grpcErr(codes.Unknown, "worker_error", "unexpected profile picture URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return nil, grpcErr(codes.Unknown, "worker_error", "bad profile picture URL")
	}
	resp, err := w.hooks().httpClient.Do(req)
	if err != nil {
		return nil, grpcErr(codes.Unavailable, "media_download_failed", "profile picture download failed")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusForbidden:
		return nil, grpcErr(codes.NotFound, "not_found", "profile picture no longer available")
	case resp.StatusCode != http.StatusOK:
		return nil, grpcErr(codes.Unavailable, "media_download_failed", fmt.Sprintf("profile picture download: HTTP %d", resp.StatusCode))
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, grpcErr(codes.Unknown, "worker_error", "cannot create picture file")
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxPictureBytes+1))
	f.Close()
	if err != nil || n > maxPictureBytes {
		os.Remove(path)
		if err == nil {
			return nil, grpcErr(codes.Unknown, "worker_error", "profile picture too large")
		}
		return nil, grpcErr(codes.Unavailable, "media_download_failed", "profile picture download interrupted")
	}
	return &workerv1.DownloadProfilePictureResponse{Id: info.ID, Size: n}, nil
}

func (w *Whatsmeow) UserInfo(ctx context.Context, raw string, business bool) (*workerv1.GetUserInfoResponse, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	jid, err := parseUser(raw)
	if err != nil {
		return nil, err
	}
	h := w.hooks()
	all, err := h.userInfo(ctx, []types.JID{jid})
	if err != nil {
		return nil, accountErr(err)
	}
	info, ok := all[jid]
	if !ok {
		return nil, grpcErr(codes.NotFound, "not_found", "contact not found on WhatsApp")
	}
	out := &workerv1.GetUserInfoResponse{Jid: w.phoneJID(jid, types.EmptyJID, nil).String(), About: info.Status, PictureId: info.PictureID}
	if !info.LID.IsEmpty() {
		out.Lid = info.LID.ToNonAD().String()
	}
	if vn := info.VerifiedName; vn != nil && vn.Details != nil {
		out.VerifiedName = vn.Details.GetVerifiedName()
	}
	if business && out.VerifiedName != "" {
		if bp, err := h.businessProfile(ctx, jid); err == nil && bp != nil {
			b := &workerv1.BusinessProfile{Address: bp.Address, Email: bp.Email}
			for _, c := range bp.Categories {
				b.Categories = append(b.Categories, c.Name)
			}
			out.Business = b
		}
	}
	return out, nil
}

// blockedJIDs lists a blocklist as phone-number JIDs when known (else the LID).
func (w *Whatsmeow) blockedJIDs(bl *types.Blocklist) []string {
	out := []string{}
	if bl == nil {
		return out
	}
	for _, it := range bl.Items {
		jid := it.PN
		if jid.IsEmpty() {
			jid = w.phoneJID(it.LID, types.EmptyJID, nil)
		}
		if !jid.IsEmpty() {
			out = append(out, jid.ToNonAD().String())
		}
	}
	sort.Strings(out)
	return out
}

func (w *Whatsmeow) Blocklist(ctx context.Context) ([]string, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	bl, err := w.hooks().getBlocklist(ctx, "")
	if err != nil {
		return nil, accountErr(err)
	}
	return w.blockedJIDs(bl), nil
}

func (w *Whatsmeow) UpdateBlocklist(ctx context.Context, raw string, block bool) ([]string, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	jid, err := parseUser(raw)
	if err != nil {
		return nil, err
	}
	action := events.BlocklistChangeActionUnblock
	if block {
		action = events.BlocklistChangeActionBlock
	}
	h := w.hooks()
	bl, err := h.updateBlocklist(ctx, jid, action, "")
	if err != nil {
		return nil, accountErr(err)
	}
	if bl == nil { // no list in the answer: read it
		if bl, err = h.getBlocklist(ctx, ""); err != nil {
			return nil, accountErr(err)
		}
	}
	return w.blockedJIDs(bl), nil
}

func (w *Whatsmeow) Profile(ctx context.Context) (*workerv1.GetProfileResponse, error) {
	pn, lid := w.ownJIDs()
	if pn.IsEmpty() {
		return nil, grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	}
	out := &workerv1.GetProfileResponse{Jid: pn.String()}
	if !lid.IsEmpty() {
		out.Lid = lid.String()
	}
	if st := w.cli.Store; st != nil {
		out.PushName, out.BusinessName, out.Platform = st.PushName, st.BusinessName, st.Platform
	}
	// About and picture id need the network; the profile is still useful without them.
	if all, err := w.hooks().userInfo(ctx, []types.JID{pn}); err == nil {
		if info, ok := all[pn]; ok {
			out.About, out.PictureId = info.Status, info.PictureID
		}
	}
	return out, nil
}

// Profile limits as enforced by WhatsApp's apps.
const (
	maxPushName = 25
	maxAbout    = 139
)

func (w *Whatsmeow) UpdateProfile(ctx context.Context, req *workerv1.UpdateProfileRequest) (*workerv1.UpdateProfileResponse, error) {
	if pn, _ := w.ownJIDs(); pn.IsEmpty() {
		return nil, grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	}
	h := w.hooks()
	out := &workerv1.UpdateProfileResponse{}
	if req.SetName {
		name := strings.TrimSpace(req.Name)
		if name == "" || len([]rune(name)) > maxPushName {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", fmt.Sprintf("name must have 1 to %d characters", maxPushName))
		}
		// whatsmeow saves the new push name when the app state echo comes back.
		if err := h.sendAppState(ctx, appstate.BuildSettingPushName(name)); err != nil {
			return nil, accountErr(err)
		}
	}
	if req.SetAbout {
		if len([]rune(req.About)) > maxAbout {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", fmt.Sprintf("about is limited to %d characters", maxAbout))
		}
		text := req.About
		if err := h.setStatus(ctx, types.SetStatusInput{Text: &text}); err != nil {
			return nil, accountErr(err)
		}
	}
	switch {
	case req.RemovePicture:
		if _, err := h.setPhoto(ctx, types.EmptyJID, nil); err != nil {
			return nil, accountErr(err)
		}
	case req.PicturePath != "":
		path, err := w.dataPath(req.PicturePath, "outbox")
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "cannot read the picture file")
		}
		// No target: WhatsApp applies w:profile:picture to the account itself.
		id, err := h.setPhoto(ctx, types.EmptyJID, data)
		if err != nil {
			return nil, accountErr(err)
		}
		out.PictureId = id
	}
	return out, nil
}

// privacyNames maps whatsmeow's privacy setting types onto the settings map.
func privacyMap(p *types.PrivacySettings) map[string]string {
	m := map[string]string{}
	if p == nil {
		return m
	}
	put := func(k types.PrivacySettingType, v types.PrivacySetting) {
		if v != "" {
			m[string(k)] = string(v)
		}
	}
	put(types.PrivacySettingTypeLastSeen, p.LastSeen)
	put(types.PrivacySettingTypeOnline, p.Online)
	put(types.PrivacySettingTypeProfile, p.Profile)
	put(types.PrivacySettingTypeStatus, p.Status)
	put(types.PrivacySettingTypeGroupAdd, p.GroupAdd)
	put(types.PrivacySettingTypeReadReceipts, p.ReadReceipts)
	put(types.PrivacySettingTypeCallAdd, p.CallAdd)
	put(types.PrivacySettingTypeMessages, p.Messages)
	return m
}

// privacyValues lists the values each settable privacy type accepts (types/user.go).
var privacyValues = map[types.PrivacySettingType][]types.PrivacySetting{
	types.PrivacySettingTypeLastSeen:     {types.PrivacySettingAll, types.PrivacySettingContacts, types.PrivacySettingContactBlacklist, types.PrivacySettingNone},
	types.PrivacySettingTypeProfile:      {types.PrivacySettingAll, types.PrivacySettingContacts, types.PrivacySettingContactBlacklist, types.PrivacySettingNone},
	types.PrivacySettingTypeStatus:       {types.PrivacySettingAll, types.PrivacySettingContacts, types.PrivacySettingContactBlacklist, types.PrivacySettingNone},
	types.PrivacySettingTypeGroupAdd:     {types.PrivacySettingAll, types.PrivacySettingContacts, types.PrivacySettingContactBlacklist, types.PrivacySettingNone},
	types.PrivacySettingTypeReadReceipts: {types.PrivacySettingAll, types.PrivacySettingNone},
	types.PrivacySettingTypeOnline:       {types.PrivacySettingAll, types.PrivacySettingMatchLastSeen},
	types.PrivacySettingTypeCallAdd:      {types.PrivacySettingAll, types.PrivacySettingKnown},
	types.PrivacySettingTypeMessages:     {types.PrivacySettingAll, types.PrivacySettingContacts},
}

// ValidPrivacy reports whether value is accepted for the privacy setting name.
func ValidPrivacy(name, value string) bool {
	for _, v := range privacyValues[types.PrivacySettingType(name)] {
		if string(v) == value {
			return true
		}
	}
	return false
}

func (w *Whatsmeow) Privacy(ctx context.Context) (map[string]string, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	p, err := w.hooks().privacy(ctx, true)
	if err != nil {
		return nil, accountErr(err)
	}
	return privacyMap(p), nil
}

func (w *Whatsmeow) SetPrivacy(ctx context.Context, req *workerv1.SetPrivacyRequest) (map[string]string, error) {
	if err := w.loggedIn(); err != nil {
		return nil, err
	}
	h := w.hooks()
	if req.SetDefaultDisappearing {
		switch req.DefaultDisappearingSeconds {
		case 0, 86400, 7 * 86400, 90 * 86400:
		default:
			return nil, grpcErr(codes.InvalidArgument, "validation_failed", "default disappearing timer must be 0, 86400, 604800 or 7776000 seconds")
		}
		if err := h.setDisappearing(ctx, time.Duration(req.DefaultDisappearingSeconds)*time.Second); err != nil {
			return nil, accountErr(err)
		}
	}
	if req.Name == "" {
		return w.Privacy(ctx)
	}
	if !ValidPrivacy(req.Name, req.Value) {
		return nil, grpcErr(codes.InvalidArgument, "validation_failed", "invalid privacy setting or value")
	}
	p, err := h.setPrivacy(ctx, types.PrivacySettingType(req.Name), types.PrivacySetting(req.Value))
	if err != nil {
		return nil, accountErr(err)
	}
	return privacyMap(&p), nil
}

// onAccount turns app state events (from any device of the account, including the echo
// of our own ChatAction) into ChatUpdate events; it reports whether evt was one.
func (w *Whatsmeow) onAccount(evt any) bool {
	var u *workerv1.ChatUpdate
	switch e := evt.(type) {
	case *events.Archive:
		u = &workerv1.ChatUpdate{Field: "archived", Value: e.Action.GetArchived()}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.Pin:
		u = &workerv1.ChatUpdate{Field: "pinned", Value: e.Action.GetPinned()}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.Mute:
		u = &workerv1.ChatUpdate{Field: "muted", Value: e.Action.GetMuted()}
		if u.Value {
			u.MutedUntilMs = e.Action.GetMuteEndTimestamp()
			if u.MutedUntilMs <= 0 {
				u.MutedUntilMs = -1
			}
		}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.MarkChatAsRead:
		u = &workerv1.ChatUpdate{Field: "unread", Value: !e.Action.GetRead()}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.DeleteChat:
		u = &workerv1.ChatUpdate{Field: "deleted", Value: true}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.ClearChat:
		u = &workerv1.ChatUpdate{Field: "cleared", Value: true}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.JID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.Star:
		u = &workerv1.ChatUpdate{Field: "starred", Value: e.Action.GetStarred(), MessageId: e.MessageID, FromMe: e.IsFromMe}
		u.Chat, u.TimestampMs, u.FullSync = w.chatUpdateJID(e.ChatJID), e.Timestamp.UnixMilli(), e.FromFullSync
	case *events.PairPasskeyRequest, *events.PairPasskeyError, *events.PairPasskeyConfirmation:
		// Passkey pairing needs a WebAuthn assertion for WhatsApp's site, which a server
		// cannot produce: use the QR code or the pairing code instead.
		w.log.Warn("passkey pairing requested by WhatsApp; not supported, pair with the QR or a phone code")
		return true
	default:
		return false
	}
	if u.Chat == "" {
		return true
	}
	w.sink.Data(&workerv1.Event{Kind: &workerv1.Event_ChatUpdate{ChatUpdate: u}})
	return true
}

// chatUpdateJID is the chat of an app state event as the log names it ("" when out of
// scope): the phone-number JID for direct chats when known.
func (w *Whatsmeow) chatUpdateJID(jid types.JID) string {
	switch jid.Server {
	case types.DefaultUserServer, types.HiddenUserServer:
		return w.phoneJID(jid, types.EmptyJID, nil).String()
	case types.GroupServer:
		return jid.ToNonAD().String()
	}
	return ""
}
