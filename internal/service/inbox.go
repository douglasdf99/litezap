package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/lookup"
	"github.com/douglasdf99/litezap/internal/store"
)

type ChatView struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name,omitempty"`
	IsGroup       bool      `json:"is_group"`
	LastMessageAt time.Time `json:"last_message_at"`
	UnreadCount   int       `json:"unread_count"`
}

type ChatPage struct {
	Chats      []ChatView `json:"chats"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type MessagePage struct {
	Messages   []MessageView `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

func encodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, v) != nil {
		return apierr.Validation("invalid cursor")
	}
	return nil
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return min(limit, 200)
}

// Chats lists the instance's chats, most recent first.
func (s *Messaging) Chats(ctx context.Context, instanceID string, limit int, cursor string) (ChatPage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return ChatPage{}, mapErr(err)
	}
	var after *store.ChatCursor
	if cursor != "" {
		after = &store.ChatCursor{}
		if err := decodeCursor(cursor, after); err != nil {
			return ChatPage{}, err
		}
	}
	limit = pageLimit(limit)
	chats, cur, err := s.Store.ListChats(ctx, instanceID, limit, after)
	if err != nil {
		return ChatPage{}, err
	}
	page := ChatPage{Chats: make([]ChatView, 0, len(chats))}
	for _, c := range chats {
		page.Chats = append(page.Chats, ChatView{JID: c.JID, Name: c.Name, IsGroup: c.IsGroup,
			LastMessageAt: c.LastMessageAt, UnreadCount: c.UnreadCount})
	}
	if len(chats) == limit {
		page.NextCursor = encodeCursor(cur)
	}
	return page, nil
}

// ChatMessages pages through a chat's log, newest first.
func (s *Messaging) ChatMessages(ctx context.Context, instanceID, chatRaw string, limit int, cursor string) (MessagePage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return MessagePage{}, mapErr(err)
	}
	chat, err := ChatJID(chatRaw)
	if err != nil {
		return MessagePage{}, err
	}
	var before *store.MessageCursor
	if cursor != "" {
		before = &store.MessageCursor{}
		if err := decodeCursor(cursor, before); err != nil {
			return MessagePage{}, err
		}
	}
	limit = pageLimit(limit)
	msgs, cur, err := s.Store.ListMessages(ctx, instanceID, chat, limit, before)
	if err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Messages: make([]MessageView, 0, len(msgs))}
	for _, m := range msgs {
		page.Messages = append(page.Messages, messageView(instanceID, m))
	}
	if len(msgs) == limit {
		page.NextCursor = encodeCursor(cur)
	}
	return page, nil
}

func (s *Messaging) client(instanceID string) (workerv1.WorkerClient, error) {
	cli, err := s.Manager.Client(instanceID)
	return cli, mapErr(err)
}

// MarkRead sends read receipts for the chat's latest received messages and clears its unread count.
func (s *Messaging) MarkRead(ctx context.Context, instanceID, chatRaw string) error {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return mapErr(err)
	}
	chat, err := ChatJID(chatRaw)
	if err != nil {
		return err
	}
	c, err := s.Store.GetChat(ctx, instanceID, chat)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("chat")
	}
	if err != nil {
		return err
	}
	msgs, err := s.Store.LatestInbound(ctx, instanceID, chat, min(max(c.UnreadCount, 1), 100))
	if err != nil {
		return err
	}
	if len(msgs) > 0 {
		cli, err := s.client(instanceID)
		if err != nil {
			return err
		}
		// In groups each receipt names one sender; in direct chats the sender is implicit.
		bySender := map[string][]string{}
		for _, m := range msgs {
			sender := ""
			if c.IsGroup {
				sender = m.SenderJID
			}
			bySender[sender] = append(bySender[sender], m.WAMessageID)
		}
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		defer cancel()
		for sender, ids := range bySender {
			if _, err := cli.MarkRead(rctx, &workerv1.MarkReadRequest{Chat: chat, Sender: sender, MessageIds: ids}); err != nil {
				return mapErr(err)
			}
		}
	}
	return s.Store.ResetUnread(ctx, instanceID, chat)
}

type ContactView struct {
	JID          string `json:"jid"`
	Name         string `json:"name,omitempty"`
	PushName     string `json:"push_name,omitempty"`
	BusinessName string `json:"business_name,omitempty"`
}

func contactView(c *workerv1.Contact) ContactView {
	return ContactView{JID: c.GetJid(), Name: c.GetName(), PushName: c.GetPushName(), BusinessName: c.GetBusinessName()}
}

func (s *Messaging) Contacts(ctx context.Context, instanceID string) ([]ContactView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	cli, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.GetContacts(rctx, &workerv1.GetContactsRequest{})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]ContactView, 0, len(resp.Contacts))
	for _, c := range resp.Contacts {
		out = append(out, contactView(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JID < out[j].JID })
	return out, nil
}

func (s *Messaging) Contact(ctx context.Context, instanceID, jidRaw string) (ContactView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return ContactView{}, mapErr(err)
	}
	jid, err := ChatJID(jidRaw)
	if err != nil {
		return ContactView{}, err
	}
	cli, err := s.client(instanceID)
	if err != nil {
		return ContactView{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.GetContact(rctx, &workerv1.GetContactRequest{Jid: jid})
	if err != nil {
		return ContactView{}, mapErr(err)
	}
	if !resp.Found {
		return ContactView{}, notFound("contact")
	}
	return contactView(resp.Contact), nil
}

type NumberView struct {
	Phone  string `json:"phone"`
	Exists bool   `json:"exists"`
	JID    string `json:"jid,omitempty"`
}

// CheckNumbers asks WhatsApp which phone numbers have an account (at most 10 per call, the size of the check bucket).
// Results are cached and uncached lookups are rate-limited per instance.
func (s *Messaging) CheckNumbers(ctx context.Context, instanceID string, phones []string) ([]NumberView, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	if len(phones) == 0 || len(phones) > 10 {
		return nil, apierr.Validation("phones must have 1 to 10 numbers")
	}
	clean := make([]string, len(phones))
	for i, p := range phones {
		d := digitsOnly(p)
		if len(d) < 10 || len(d) > 15 {
			return nil, apierr.Validation("phone must have 10 to 15 digits including country code: " + p)
		}
		clean[i] = d
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	res, err := s.Resolver.Resolve(rctx, instanceID, clean, lookup.ForCheck)
	if errors.Is(err, lookup.ErrRateLimited) {
		return nil, apierr.New(http.StatusTooManyRequests, "rate_limited", err.Error(), true)
	}
	if errors.Is(err, lookup.ErrTooManyNumbers) {
		return nil, apierr.Validation("too many uncached numbers in one request; send at most 10 phones per call")
	}
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]NumberView, 0, len(res))
	for _, r := range res {
		out = append(out, NumberView{Phone: r.Phone, Exists: r.Found, JID: r.JID})
	}
	return out, nil
}

// Media returns a received attachment, downloading it through the worker the first time.
// Concurrent first reads of the same media share one download.
func (s *Messaging) Media(ctx context.Context, instanceID, mediaID string) (MediaFile, error) {
	md, err := s.Store.GetMedia(ctx, instanceID, mediaID)
	if errors.Is(err, store.ErrNotFound) {
		return MediaFile{}, notFound("media")
	}
	if err != nil {
		return MediaFile{}, err
	}
	f := MediaFile{Path: md.Path, Mime: md.Mime, FileName: md.FileName}
	if md.Path != "" {
		if _, err := os.Stat(md.Path); err == nil {
			return f, nil
		}
	}
	key := instanceID + "/" + mediaID
	s.flightMu.Lock()
	if s.flights == nil {
		s.flights = map[string]*mediaFlight{}
	}
	if fl, ok := s.flights[key]; ok {
		s.flightMu.Unlock()
		select {
		case <-fl.done:
			return fl.file, fl.err
		case <-ctx.Done():
			return MediaFile{}, ctx.Err()
		}
	}
	fl := &mediaFlight{done: make(chan struct{})}
	s.flights[key] = fl
	s.flightMu.Unlock()
	// The download outlives a caller that gives up: others may be waiting for it.
	fl.file, fl.err = s.download(context.WithoutCancel(ctx), instanceID, md, f)
	s.flightMu.Lock()
	delete(s.flights, key)
	s.flightMu.Unlock()
	close(fl.done)
	return fl.file, fl.err
}

// download has the worker write the attachment to a fresh temporary file next to its final
// path (no media over gRPC), then renames it into place.
func (s *Messaging) download(ctx context.Context, instanceID string, md store.Media, f MediaFile) (MediaFile, error) {
	cli, err := s.client(instanceID)
	if err != nil {
		return MediaFile{}, err
	}
	dir := filepath.Join(s.DataDir, "media", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MediaFile{}, err
	}
	tmp, err := os.CreateTemp(dir, md.ID+"-*.part")
	if err != nil {
		return MediaFile{}, err
	}
	part := tmp.Name()
	tmp.Close()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := cli.DownloadMedia(dctx, &workerv1.DownloadMediaRequest{Kind: md.Kind, Ref: md.Ref, DestPath: part}); err != nil {
		os.Remove(part)
		return MediaFile{}, mapErr(err)
	}
	f.Path = filepath.Join(dir, md.ID)
	if err := os.Rename(part, f.Path); err != nil {
		os.Remove(part)
		return MediaFile{}, err
	}
	retention := s.MediaRetention
	if retention == 0 {
		retention = 7 * 24 * time.Hour
	}
	if err := s.Store.SetMediaFile(ctx, md.ID, f.Path, time.Now().Add(retention)); err != nil {
		return MediaFile{}, err
	}
	return f, nil
}

// SignedMediaURL returns a temporary URL that downloads the attachment without a token.
func (s *Messaging) SignedMediaURL(ctx context.Context, instanceID, mediaID string) (string, time.Time, error) {
	if !s.SignedURLs {
		return "", time.Time{}, apierr.New(http.StatusConflict, "conflict",
			"signed URLs need public_url when the API is not bound to loopback", false)
	}
	if _, err := s.Store.GetMedia(ctx, instanceID, mediaID); errors.Is(err, store.ErrNotFound) {
		return "", time.Time{}, notFound("media")
	} else if err != nil {
		return "", time.Time{}, err
	}
	u, exp := s.Signer.URL(instanceID, mediaID)
	return u, exp, nil
}

// VerifyMediaSignature checks the exp/sig of a signed media URL.
func (s *Messaging) VerifyMediaSignature(instanceID, mediaID, exp, sig string) bool {
	return s.Signer.Verify(instanceID, mediaID, exp, sig)
}
