package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/ipallow"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
)

// Platform holds the marco 11 use cases: token authentication with IP allowlists and
// scoped API keys, instance metadata, filtered listing and bulk operations.
type Platform struct {
	Store     *store.Store
	Instances *Instances
	// Now is the clock (default time.Now).
	Now func() time.Time

	touched sync.Map // key ID -> time.Time of the last last_used_at write
}

func (p *Platform) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// API key scopes.
const (
	ScopeRead = "read" // read routes of the instance (no QR, settings or agent hook)
	ScopeSend = "send" // send messages and read their status
)

// Auth is an authenticated instance credential: an instance token (Scopes nil: full
// access to the instance) or a scoped API key.
type Auth struct {
	InstanceID  string
	IPAllowlist ipallow.List // empty: any address
	KeyID       string
	Scopes      []string
}

// Authenticate resolves an instance token (lzi_) or an API key (lzk_).
func (p *Platform) Authenticate(ctx context.Context, token string) (Auth, error) {
	switch {
	case strings.HasPrefix(token, "lzi_"):
		in, err := p.Store.InstanceByTokenHash(ctx, secure.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			return Auth{}, apierr.Unauthorized()
		}
		if err != nil {
			return Auth{}, err
		}
		allow, err := storedList(in.IPAllowlist)
		if err != nil {
			return Auth{}, err
		}
		return Auth{InstanceID: in.ID, IPAllowlist: allow}, nil
	case strings.HasPrefix(token, "lzk_"):
		k, err := p.Store.APIKeyByHash(ctx, secure.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			return Auth{}, apierr.Unauthorized()
		}
		if err != nil {
			return Auth{}, err
		}
		now := p.now()
		if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
			return Auth{}, apierr.Unauthorized()
		}
		allow, err := storedList(k.IPAllowlist)
		if err != nil {
			return Auth{}, err
		}
		p.touch(ctx, k.ID, now)
		return Auth{InstanceID: k.InstanceID, IPAllowlist: allow, KeyID: k.ID, Scopes: k.Scopes}, nil
	}
	return Auth{}, apierr.Unauthorized()
}

// storedList parses a list saved by this package; a corrupt one fails closed.
func storedList(v []string) (ipallow.List, error) {
	l, err := ipallow.Parse(v)
	if err != nil {
		return nil, fmt.Errorf("stored ip_allowlist: %w", err)
	}
	return l, nil
}

// touch records last_used_at at most once a minute per key (each write is a disk write).
func (p *Platform) touch(ctx context.Context, id string, now time.Time) {
	if v, ok := p.touched.Load(id); ok && now.Sub(v.(time.Time)) < time.Minute {
		return
	}
	p.touched.Store(id, now)
	_ = p.Store.TouchAPIKey(context.WithoutCancel(ctx), id, now)
}

// InstancePatch is PATCH /instances/{id} (nil: unchanged).
type InstancePatch struct {
	Name        *string   `json:"name"`
	Tags        *[]string `json:"tags"`
	IPAllowlist *[]string `json:"ip_allowlist"`
}

const (
	maxNameLen = 100
	maxTags    = 20
)

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,39}$`)

// NormalizeTags lowercases, validates and de-duplicates tags (keeping their order).
func NormalizeTags(in []string) ([]string, error) {
	if len(in) > maxTags {
		return nil, apierr.Validation(fmt.Sprintf("at most %d tags", maxTags))
	}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if !tagRe.MatchString(t) {
			return nil, apierr.Validation("tags must be 1-40 characters: letters, digits and _ . : - (starting with a letter or digit)")
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxNameLen {
		return "", apierr.Validation(fmt.Sprintf("name is required (up to %d characters)", maxNameLen))
	}
	return name, nil
}

func parseAllowlist(field string, v []string) ([]string, error) {
	l, err := ipallow.Parse(v)
	if err != nil {
		return nil, apierr.Validation(field + ": " + err.Error())
	}
	return l.Strings(), nil
}

// Update renames an instance and sets its tags and IP allowlist. Permission checks (who
// may change which field) are the caller's.
func (p *Platform) Update(ctx context.Context, id string, patch InstancePatch) (InstanceView, error) {
	var m store.InstanceMeta
	if patch.Name != nil {
		name, err := validName(*patch.Name)
		if err != nil {
			return InstanceView{}, err
		}
		m.Name = &name
	}
	if patch.Tags != nil {
		tags, err := NormalizeTags(*patch.Tags)
		if err != nil {
			return InstanceView{}, err
		}
		m.Tags = &tags
	}
	if patch.IPAllowlist != nil {
		l, err := parseAllowlist("ip_allowlist", *patch.IPAllowlist)
		if err != nil {
			return InstanceView{}, err
		}
		m.IPAllowlist = &l
	}
	if err := p.Store.UpdateInstanceMeta(ctx, id, m); err != nil {
		return InstanceView{}, mapErr(err)
	}
	return p.Instances.Get(ctx, id)
}

// InstanceQuery filters GET /instances.
type InstanceQuery struct {
	States []string
	Tag    string
	Search string
	Limit  int    // 0: everything (no pagination)
	Cursor string // next_cursor of the previous page
}

// InstancePage is a page of instances; NextCursor is empty on the last page.
type InstancePage struct {
	Instances  []InstanceView `json:"instances"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

const maxPageLimit = 500

var knownStates = []instance.State{instance.Created, instance.Starting, instance.QRPending, instance.Connecting,
	instance.Connected, instance.Reconnecting, instance.LoggedOut, instance.Banned, instance.Failed, instance.Stopped}

// Search lists instances with filters and optional pagination.
func (p *Platform) Search(ctx context.Context, q InstanceQuery) (InstancePage, error) {
	sq := store.InstanceQuery{Search: strings.TrimSpace(q.Search)}
	for _, st := range q.States {
		if !slices.Contains(knownStates, instance.State(st)) {
			return InstancePage{}, apierr.Validation("unknown state " + fmt.Sprintf("%q", st))
		}
		sq.States = append(sq.States, st)
	}
	if q.Tag != "" {
		sq.Tag = strings.ToLower(strings.TrimSpace(q.Tag))
	}
	if q.Limit < 0 || q.Limit > maxPageLimit {
		return InstancePage{}, apierr.Validation(fmt.Sprintf("limit must be between 1 and %d", maxPageLimit))
	}
	if q.Cursor != "" {
		if q.Limit == 0 {
			return InstancePage{}, apierr.Validation("cursor needs limit")
		}
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		created, id, ok := strings.Cut(string(b), "|")
		if err != nil || !ok || id == "" {
			return InstancePage{}, apierr.Validation("invalid cursor")
		}
		sq.AfterCreated, sq.AfterID = created, id
	}
	if q.Limit > 0 {
		sq.Limit = q.Limit + 1 // one more tells whether there is a next page
	}
	list, err := p.Store.ListInstancesPage(ctx, sq)
	if err != nil {
		return InstancePage{}, err
	}
	page := InstancePage{Instances: make([]InstanceView, 0, len(list))}
	if q.Limit > 0 && len(list) > q.Limit {
		list = list[:q.Limit]
		last := list[len(list)-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(store.FormatTS(last.CreatedAt) + "|" + last.ID))
	}
	for _, in := range list {
		page.Instances = append(page.Instances, view(in))
	}
	return page, nil
}

// BulkRequest is POST /instances/bulk: one action on instances given by ID or by tag.
type BulkRequest struct {
	Action  string   `json:"action"`
	IDs     []string `json:"ids"`
	Tag     string   `json:"tag"`
	Confirm bool     `json:"confirm"`
}

// BulkResult is the outcome for one instance.
type BulkResult struct {
	ID    string        `json:"id"`
	OK    bool          `json:"ok"`
	Error *apierr.Error `json:"error,omitempty"`
}

// MaxBulk bounds one bulk request.
const MaxBulk = 100

var bulkActions = []string{"start", "stop", "restart", "delete"}

// Bulk runs the action on every instance (a few at a time) and reports each outcome; one
// failure does not stop the others.
func (p *Platform) Bulk(ctx context.Context, req BulkRequest) ([]BulkResult, error) {
	if !slices.Contains(bulkActions, req.Action) {
		return nil, apierr.Validation("action must be one of start, stop, restart, delete")
	}
	if (len(req.IDs) == 0) == (req.Tag == "") {
		return nil, apierr.Validation("give either ids or tag")
	}
	if req.Action == "delete" && !req.Confirm {
		return nil, apierr.Validation(`delete needs "confirm": true (it removes the instances and their data)`)
	}
	ids := []string{}
	if req.Tag != "" {
		list, err := p.Store.ListInstancesPage(ctx, store.InstanceQuery{Tag: strings.ToLower(strings.TrimSpace(req.Tag)), Limit: MaxBulk + 1})
		if err != nil {
			return nil, err
		}
		for _, in := range list {
			ids = append(ids, in.ID)
		}
	} else {
		for _, id := range req.IDs {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > MaxBulk {
		return nil, apierr.Validation(fmt.Sprintf("at most %d instances per request", MaxBulk))
	}
	fn := map[string]func(context.Context, string) error{
		"start": p.Instances.Start, "stop": p.Instances.Stop, "restart": p.Instances.Restart, "delete": p.Instances.Delete,
	}[req.Action]
	// A client disconnect must not leave half of the batch undone without a report.
	ctx = context.WithoutCancel(ctx)
	out := make([]BulkResult, len(ids))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = BulkResult{ID: id, OK: true}
			if err := fn(ctx, id); err != nil {
				var e *apierr.Error
				if !errors.As(err, &e) {
					e = apierr.Internal()
				}
				out[i] = BulkResult{ID: id, Error: e}
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// KeyInput creates an API key.
type KeyInput struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	IPAllowlist []string `json:"ip_allowlist"`
	// ExpiresInSeconds, when > 0, makes the key stop working after that long.
	ExpiresInSeconds int64 `json:"expires_in_seconds"`
}

// KeyView is an API key without its secret.
type KeyView struct {
	ID          string     `json:"id"`
	InstanceID  string     `json:"instance_id"`
	Name        string     `json:"name"`
	Scopes      []string   `json:"scopes"`
	IPAllowlist []string   `json:"ip_allowlist"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

func keyView(k store.APIKey) KeyView {
	v := KeyView{ID: k.ID, InstanceID: k.InstanceID, Name: k.Name, Scopes: k.Scopes, IPAllowlist: k.IPAllowlist, CreatedAt: k.CreatedAt}
	if v.Scopes == nil {
		v.Scopes = []string{}
	}
	if !k.ExpiresAt.IsZero() {
		t := k.ExpiresAt.UTC()
		v.ExpiresAt = &t
	}
	if !k.LastUsedAt.IsZero() {
		t := k.LastUsedAt.UTC()
		v.LastUsedAt = &t
	}
	return v
}

// MaxKeysPerInstance bounds the keys of one instance.
const MaxKeysPerInstance = 50

// CreateKey mints a scoped key for the instance and returns it once (only its hash is kept).
func (p *Platform) CreateKey(ctx context.Context, instanceID string, in KeyInput) (KeyView, string, error) {
	name, err := validName(in.Name)
	if err != nil {
		return KeyView{}, "", err
	}
	scopes := []string{}
	for _, s := range in.Scopes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != ScopeRead && s != ScopeSend {
			return KeyView{}, "", apierr.Validation(`scopes must be "read" and/or "send"`)
		}
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 0 {
		return KeyView{}, "", apierr.Validation(`scopes is required ("read" and/or "send")`)
	}
	slices.Sort(scopes)
	allow, err := parseAllowlist("ip_allowlist", in.IPAllowlist)
	if err != nil {
		return KeyView{}, "", err
	}
	if in.ExpiresInSeconds < 0 || in.ExpiresInSeconds > 10*365*24*3600 {
		return KeyView{}, "", apierr.Validation("expires_in_seconds must be between 0 (never) and 10 years")
	}
	if _, err := p.Store.GetInstance(ctx, instanceID); err != nil {
		return KeyView{}, "", mapErr(err)
	}
	if n, err := p.Store.CountAPIKeys(ctx, instanceID); err != nil {
		return KeyView{}, "", err
	} else if n >= MaxKeysPerInstance {
		return KeyView{}, "", apierr.New(http.StatusConflict, "limit_reached", fmt.Sprintf("an instance has at most %d API keys", MaxKeysPerInstance), false)
	}
	b := make([]byte, 32)
	rand.Read(b)
	secret := "lzk_" + base64.RawURLEncoding.EncodeToString(b)
	k := store.APIKey{ID: secure.NewID("key_"), InstanceID: instanceID, Name: name, KeyHash: secure.HashToken(secret),
		Scopes: scopes, IPAllowlist: allow}
	if in.ExpiresInSeconds > 0 {
		k.ExpiresAt = p.now().Add(time.Duration(in.ExpiresInSeconds) * time.Second)
	}
	if err := p.Store.CreateAPIKey(ctx, k); err != nil {
		return KeyView{}, "", err
	}
	k.CreatedAt = p.now()
	return keyView(k), secret, nil
}

func (p *Platform) ListKeys(ctx context.Context, instanceID string) ([]KeyView, error) {
	if _, err := p.Store.GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	keys, err := p.Store.ListAPIKeys(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	out := make([]KeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyView(k))
	}
	return out, nil
}

func (p *Platform) DeleteKey(ctx context.Context, instanceID, keyID string) error {
	err := p.Store.DeleteAPIKey(ctx, instanceID, keyID)
	if errors.Is(err, store.ErrNotFound) {
		return apierr.New(http.StatusNotFound, "not_found", "API key not found", false)
	}
	p.touched.Delete(keyID)
	return err
}
