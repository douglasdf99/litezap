package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// WhatsApp events (MSG-24). An event is an EventMessage carrying a message secret, like a
// poll. Responses (going / not going / maybe) and edits are encrypted with keys derived
// from that secret: EncEventResponseMessage ("Event Response") and SecretEncryptedMessage
// of type EVENT_EDIT ("Event Edit"). whatsmeow decrypts edits but neither encrypts them
// nor handles responses, so the derivation of msgsecret.go (generateMsgSecretKey) is
// reproduced here.

const (
	secretEventResponse = "Event Response"
	secretEventEdit     = "Event Edit"
	secretPollVote      = "Poll Vote"
)

// msgSecretKey derives the key (and the additional data) of a message secret use case:
// HKDF-SHA256 over the original secret with info = original id, original sender,
// modification sender and use case.
func msgSecretKey(useCase string, modSender types.JID, origID string, origSender types.JID, secret []byte) (key, ad []byte) {
	o, m := origSender.ToNonAD().String(), modSender.ToNonAD().String()
	info := make([]byte, 0, len(origID)+len(o)+len(m)+len(useCase))
	info = append(append(append(append(info, origID...), o...), m...), useCase...)
	key = hkdfutil.SHA256(secret, nil, info, 32)
	if useCase == secretEventResponse || useCase == secretPollVote {
		ad = fmt.Appendf(nil, "%s\x00%s", origID, m)
	}
	return key, ad
}

func eventInfo(e *workerv1.EventInfo) (*waE2E.EventMessage, error) {
	if e == nil || strings.TrimSpace(e.Name) == "" || e.StartTime <= 0 {
		return nil, invalid("an event needs a name and a start time")
	}
	if e.EndTime != 0 && e.EndTime < e.StartTime {
		return nil, invalid("an event cannot end before it starts")
	}
	em := &waE2E.EventMessage{Name: proto.String(e.Name), StartTime: proto.Int64(e.StartTime),
		IsCanceled: proto.Bool(e.Canceled), ExtraGuestsAllowed: proto.Bool(e.ExtraGuestsAllowed)}
	if e.Description != "" {
		em.Description = proto.String(e.Description)
	}
	if e.EndTime > 0 {
		em.EndTime = proto.Int64(e.EndTime)
	}
	if e.JoinLink != "" {
		em.JoinLink = proto.String(e.JoinLink)
	}
	if l := e.Location; l != nil {
		lm := &waE2E.LocationMessage{}
		if l.Latitude != 0 || l.Longitude != 0 {
			lm.DegreesLatitude, lm.DegreesLongitude = proto.Float64(l.Latitude), proto.Float64(l.Longitude)
		}
		if l.Name != "" {
			lm.Name = proto.String(l.Name)
		}
		if l.Address != "" {
			lm.Address = proto.String(l.Address)
		}
		em.Location = lm
	}
	return em, nil
}

// buildEvent is a new event, with the message secret its responses and edits use
// (SendMessage stores it).
func buildEvent(req *workerv1.SendRequest) (*waE2E.Message, error) {
	em, err := eventInfo(req.Event)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &waE2E.Message{EventMessage: em, MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret}}, nil
}

// identities are the JIDs (non-AD) a user may be named by: j, alt, and its other
// addressing (phone number <-> LID) when known. In LID-addressed groups the key of the
// original message, the sender of an update and the identities the other phone feeds
// into the key derivation do not always agree (phone number on one side, LID on the
// other), so secrets are looked up and keys derived under every identity.
func (w *Whatsmeow) identities(ctx context.Context, j, alt types.JID) []types.JID {
	var out []types.JID
	add := func(x types.JID) {
		if x.User == "" {
			return
		}
		if x = x.ToNonAD(); !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	add(j)
	add(alt)
	if w.isOwn(j) {
		pn, lid := w.ownJIDs()
		add(pn)
		add(lid)
		return out
	}
	if len(out) > 1 || w.cli == nil || w.cli.Store == nil || w.cli.Store.LIDs == nil {
		return out
	}
	switch j.Server {
	case types.HiddenUserServer:
		if pn, err := w.cli.Store.LIDs.GetPNForLID(ctx, j.ToNonAD()); err == nil {
			add(pn)
		}
	case types.DefaultUserServer:
		if lid, err := w.cli.Store.LIDs.GetLIDForPN(ctx, j.ToNonAD()); err == nil {
			add(lid)
		}
	}
	return out
}

// lookupSecret finds the message secret of id stored under any of authors (nil: not
// stored) and the sender it was stored with.
func (w *Whatsmeow) lookupSecret(ctx context.Context, chat types.JID, authors []types.JID, id string) ([]byte, types.JID, error) {
	if w.cli == nil || w.cli.Store == nil || w.cli.Store.MsgSecrets == nil {
		return nil, types.EmptyJID, errors.New("message secret store unavailable")
	}
	for _, a := range authors {
		secret, stored, err := w.cli.Store.MsgSecrets.GetMessageSecret(ctx, chat, a, types.MessageID(id))
		if err != nil {
			return nil, types.EmptyJID, err
		}
		if secret != nil {
			if stored.IsEmpty() {
				stored = a
			}
			return secret, stored, nil
		}
	}
	return nil, types.EmptyJID, nil
}

// eventSecret finds the secret of an event (ours or a contact's) and the sender it was
// stored with. author is the event's sender as the log knows it.
func (w *Whatsmeow) eventSecret(ctx context.Context, chat, author types.JID, id string) ([]byte, types.JID, error) {
	if w.cli == nil || w.cli.Store == nil || w.cli.Store.MsgSecrets == nil {
		return nil, types.EmptyJID, grpcErr(codes.Unknown, "worker_error", "message secret store unavailable")
	}
	secret, stored, err := w.lookupSecret(ctx, chat, w.identities(ctx, author, types.EmptyJID), id)
	if err != nil {
		return nil, types.EmptyJID, grpcErr(codes.Unknown, "worker_error", "cannot read the event's key")
	}
	if secret == nil {
		return nil, types.EmptyJID, grpcErr(codes.FailedPrecondition, "event_secret_missing",
			"the event's key is not known to this session (events received before pairing cannot be answered or edited)")
	}
	return secret, stored, nil
}

// ownFor is the account's JID in the addressing of author (phone number or LID), as
// whatsmeow's EncryptPollVote picks it.
func (w *Whatsmeow) ownFor(author types.JID) types.JID {
	pn, lid := w.ownJIDs()
	if author.Server == types.DefaultUserServer || lid.IsEmpty() {
		return pn
	}
	return lid
}

func sealSecret(key, ad, plaintext []byte) (ct, iv []byte, err error) {
	iv = make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, nil, err
	}
	ct, err = gcmutil.Encrypt(key, iv, plaintext, ad)
	return ct, iv, err
}

// eventKey is the message key of the event a response or edit refers to.
func eventKey(to types.JID, id string, fromMe bool, author types.JID) *waCommon.MessageKey {
	k := &waCommon.MessageKey{RemoteJID: proto.String(to.String()), FromMe: proto.Bool(fromMe), ID: proto.String(id)}
	if to.Server == types.GroupServer && !author.IsEmpty() {
		k.Participant = proto.String(author.ToNonAD().String())
	}
	return k
}

// buildEventEdit is an edit (or cancellation) of one of our events, encrypted as
// WhatsApp sends it (SecretEncryptedMessage EVENT_EDIT).
func (w *Whatsmeow) buildEventEdit(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	if req.TargetId == "" || !req.TargetFromMe {
		return nil, invalid("only your own events can be edited")
	}
	em, err := eventInfo(req.Event)
	if err != nil {
		return nil, err
	}
	pn, _ := w.ownJIDs()
	secret, author, err := w.eventSecret(ctx, to, pn, req.TargetId)
	if err != nil {
		return nil, err
	}
	plaintext, err := proto.Marshal(&waE2E.Message{EventMessage: em})
	if err != nil {
		return nil, err
	}
	key, ad := msgSecretKey(secretEventEdit, w.ownFor(author), req.TargetId, author, secret)
	ct, iv, err := sealSecret(key, ad, plaintext)
	if err != nil {
		return nil, err
	}
	return &waE2E.Message{SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
		TargetMessageKey: eventKey(to, req.TargetId, true, author),
		EncPayload:       ct, EncIV: iv,
		SecretEncType: waE2E.SecretEncryptedMessage_EVENT_EDIT.Enum(),
	}}, nil
}

var eventResponses = map[string]waE2E.EventResponseMessage_EventResponseType{
	"going": waE2E.EventResponseMessage_GOING, "not_going": waE2E.EventResponseMessage_NOT_GOING, "maybe": waE2E.EventResponseMessage_MAYBE,
}

func eventResponseName(t waE2E.EventResponseMessage_EventResponseType) string {
	for k, v := range eventResponses {
		if v == t {
			return k
		}
	}
	return "unknown"
}

// buildEventResponse answers an event (ours or a contact's), encrypted like a poll vote.
func (w *Whatsmeow) buildEventResponse(ctx context.Context, to types.JID, req *workerv1.SendRequest) (*waE2E.Message, error) {
	resp, ok := eventResponses[req.EventResponse]
	if !ok || req.TargetId == "" {
		return nil, invalid("event_response must be going, not_going or maybe, for an event")
	}
	if req.ExtraGuests < 0 || req.ExtraGuests > 20 {
		return nil, invalid("extra_guests must be between 0 and 20")
	}
	author, _ := w.ownJIDs()
	if !req.TargetFromMe {
		var err error
		switch {
		case to.Server == types.GroupServer:
			if author, err = parseOptJID(req.TargetSender); err != nil || author.IsEmpty() {
				return nil, invalid("the event's author is required in groups")
			}
		default:
			author = to
		}
	}
	secret, stored, err := w.eventSecret(ctx, to, author, req.TargetId)
	if err != nil {
		return nil, err
	}
	r := &waE2E.EventResponseMessage{Response: resp.Enum(), TimestampMS: proto.Int64(time.Now().UnixMilli())}
	if req.ExtraGuests > 0 {
		r.ExtraGuestCount = proto.Int32(req.ExtraGuests)
	}
	plaintext, err := proto.Marshal(r)
	if err != nil {
		return nil, err
	}
	key, ad := msgSecretKey(secretEventResponse, w.ownFor(stored), req.TargetId, stored, secret)
	ct, iv, err := sealSecret(key, ad, plaintext)
	if err != nil {
		return nil, err
	}
	return &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
		EventCreationMessageKey: eventKey(to, req.TargetId, req.TargetFromMe, stored),
		EncPayload:              ct, EncIV: iv,
	}}, nil
}

// eventOf converts a received EventMessage.
func eventOf(em *waE2E.EventMessage) *workerv1.EventInfo {
	if em == nil {
		return nil
	}
	e := &workerv1.EventInfo{Name: em.GetName(), Description: em.GetDescription(), JoinLink: em.GetJoinLink(),
		StartTime: em.GetStartTime(), EndTime: em.GetEndTime(), Canceled: em.GetIsCanceled(), ExtraGuestsAllowed: em.GetExtraGuestsAllowed()}
	if l := em.GetLocation(); l != nil {
		e.Location = &workerv1.Location{Latitude: l.GetDegreesLatitude(), Longitude: l.GetDegreesLongitude(), Name: l.GetName(), Address: l.GetAddress()}
	}
	return e
}

// origSenderOf is the author of the message a key refers to, from the point of view of
// the message msg that carries the key (whatsmeow's getOrigSenderFromKey).
func origSenderOf(info types.MessageInfo, key *waCommon.MessageKey) (types.JID, error) {
	switch {
	case key.GetFromMe():
		return info.Sender, nil
	case info.Chat.Server == types.DefaultUserServer || info.Chat.Server == types.HiddenUserServer:
		return types.ParseJID(key.GetRemoteJID())
	default:
		return types.ParseJID(key.GetParticipant())
	}
}

// Why a secret-encrypted update (event response, poll vote) could not be opened. These
// errors never carry JIDs or content: they end up in the logs.
var (
	errSecretMissing = errors.New("secret_missing: the original message's secret is not stored (sent before pairing?)")
	errNoKeyMatched  = errors.New("no_key_matched: no sender identity opens the payload")
	errBadKey        = errors.New("bad_key: the update does not name its original message")
)

// secretFailure is the short reason logged for a decryption error.
func secretFailure(err error) string {
	for _, e := range []error{errSecretMissing, errNoKeyMatched, errBadKey} {
		if errors.Is(err, e) {
			r, _, _ := strings.Cut(e.Error(), ":")
			return r
		}
	}
	return "error"
}

// openSecret decrypts the payload of an update (useCase) that refers to the message key.
// Unlike whatsmeow's decryptMsgSecret (the update's sender as is, two original senders),
// it tries every identity of the original sender and of the update's sender: in
// LID-addressed groups the responder's phone may derive the key with phone numbers while
// the update reaches us addressed by LID, or the other way round. AES-GCM authenticates
// the result, so a wrong combination never yields a plaintext.
func (w *Whatsmeow) openSecret(ctx context.Context, e *events.Message, useCase string, key *waCommon.MessageKey, iv, payload []byte) ([]byte, error) {
	if key.GetID() == "" {
		return nil, errBadKey
	}
	orig, err := origSenderOf(e.Info, key)
	if err != nil || orig.User == "" {
		return nil, errBadKey
	}
	origs := w.identities(ctx, orig, types.EmptyJID)
	secret, stored, err := w.lookupSecret(ctx, e.Info.Chat, origs, key.GetID())
	if err != nil {
		return nil, fmt.Errorf("cannot read the message secret: %w", err)
	}
	if secret == nil {
		return nil, errSecretMissing
	}
	for _, s := range w.identities(ctx, stored, types.EmptyJID) {
		if !slices.Contains(origs, s) {
			origs = append(origs, s)
		}
	}
	mods := w.identities(ctx, e.Info.Sender, e.Info.SenderAlt)
	for _, o := range origs {
		for _, m := range mods {
			k, ad := msgSecretKey(useCase, m, key.GetID(), o, secret)
			if plain, err := gcmutil.Decrypt(k, iv, payload, ad); err == nil {
				return plain, nil
			}
		}
	}
	return nil, fmt.Errorf("%w (%d combinations)", errNoKeyMatched, len(origs)*len(mods))
}

// decryptEventResponse opens a received EncEventResponseMessage.
func (w *Whatsmeow) decryptEventResponse(ctx context.Context, e *events.Message) (*waE2E.EventResponseMessage, error) {
	enc := e.Message.GetEncEventResponseMessage()
	plain, err := w.openSecret(ctx, e, secretEventResponse, enc.GetEventCreationMessageKey(), enc.GetEncIV(), enc.GetEncPayload())
	if err != nil {
		return nil, err
	}
	var r waE2E.EventResponseMessage
	if err := proto.Unmarshal(plain, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// decryptPollVote opens a received PollUpdateMessage, like whatsmeow's DecryptPollVote
// but under every sender identity (see openSecret).
func (w *Whatsmeow) decryptPollVote(ctx context.Context, e *events.Message) (*waE2E.PollVoteMessage, error) {
	pu := e.Message.GetPollUpdateMessage()
	plain, err := w.openSecret(ctx, e, secretPollVote, pu.GetPollCreationMessageKey(), pu.GetVote().GetEncIV(), pu.GetVote().GetEncPayload())
	if err != nil {
		return nil, err
	}
	var v waE2E.PollVoteMessage
	if err := proto.Unmarshal(plain, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
