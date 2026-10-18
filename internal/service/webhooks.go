package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/store"
	"github.com/douglasdf99/litezap/internal/webhook"
)

// Scope is who calls: the operator (managed hosting; always also Admin), the admin, or
// the token of one instance.
type Scope struct {
	Admin      bool
	Operator   bool
	InstanceID string
}

// Webhooks manages webhook registrations and their deliveries (spec 5.3). An instance
// token sees only its own instance's webhooks; another instance's webhook is not found.
type Webhooks struct {
	Store      *store.Store
	Dispatcher *webhook.Dispatcher
}

// WebhookInput is a create or patch request; nil fields are left unchanged (patch) or
// take their defaults (create).
type WebhookInput struct {
	URL        *string  `json:"url"`
	Secret     *string  `json:"secret"`
	Events     []string `json:"events"`
	InstanceID *string  `json:"instance_id"`
	Version    *int     `json:"version"`
	Enabled    *bool    `json:"enabled"`
	// Filters narrow the events by chat, message type and from_me ({} clears them).
	Filters *events.Filters `json:"filters"`
}

type WebhookView struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id,omitempty"` // empty: every instance
	URL        string   `json:"url"`
	Events     []string `json:"events"`
	Version    int      `json:"version"`
	Enabled    bool     `json:"enabled"`
	// Filters is omitted when the webhook has none.
	Filters   *events.Filters `json:"filters,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	// Secret is returned only when it is set (creation, or a patch that changes it).
	Secret string `json:"secret,omitempty"`
}

type DeliveryView struct {
	ID             string          `json:"id"`
	WebhookID      string          `json:"webhook_id"`
	EventID        string          `json:"event_id"`
	EventType      string          `json:"event_type"`
	InstanceID     string          `json:"instance_id,omitempty"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  *time.Time      `json:"next_attempt_at,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at"`
	LastStatusCode int             `json:"last_status_code,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeliveredAt    *time.Time      `json:"delivered_at,omitempty"`
	Event          json.RawMessage `json:"event"`
}

type DeliveryPage struct {
	Deliveries []DeliveryView `json:"deliveries"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func webhookView(w store.Webhook) WebhookView {
	v := WebhookView{ID: w.ID, InstanceID: w.InstanceID, URL: w.URL, Events: w.Events, Version: w.Version,
		Enabled: w.Enabled, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
	if f, err := events.ParseFilters(w.FiltersJSON); err == nil && !f.Empty() {
		v.Filters = &f
	}
	return v
}

// validateFilters normalizes webhook filters and returns their stored form.
func validateFilters(f *events.Filters) (string, error) {
	if f == nil {
		return "", nil
	}
	n, err := f.Normalize()
	if err != nil {
		return "", apierr.Validation(err.Error())
	}
	return n.JSON(), nil
}

func deliveryView(d store.Delivery) DeliveryView {
	v := DeliveryView{ID: d.ID, WebhookID: d.WebhookID, EventID: d.EventID, EventType: d.EventType, InstanceID: d.InstanceID,
		Status: d.Status, Attempts: d.Attempts, ExpiresAt: d.ExpiresAt, LastStatusCode: d.LastStatusCode,
		LastError: d.LastError, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Event: json.RawMessage(d.Payload)}
	if d.Status == store.DeliveryPending {
		t := d.NextAttemptAt
		v.NextAttemptAt = &t
	}
	if !d.DeliveredAt.IsZero() {
		t := d.DeliveredAt
		v.DeliveredAt = &t
	}
	return v
}

func webhookNotFound() *apierr.Error { return notFound("webhook") }

func validateURL(raw string) error {
	if len(raw) > 2048 {
		return apierr.Validation("url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return apierr.Validation("url must be an absolute http(s) URL")
	}
	return nil
}

func validateEvents(evs []string) ([]string, error) {
	if len(evs) == 0 {
		return nil, apierr.Validation(`events is required (e.g. ["message.*"] or ["*"])`)
	}
	if len(evs) > 32 {
		return nil, apierr.Validation("too many event filters")
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range evs {
		e = strings.TrimSpace(e)
		if !events.ValidPattern(e) {
			return nil, apierr.Validation("unknown event filter " + strconv.Quote(e) + "; valid types: " +
				strings.Join(events.Types, ", ") + ` (or "family.*", "*")`)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

func validateSecret(s string) error {
	if len(s) < 16 || len(s) > 256 {
		return apierr.Validation("secret must have 16 to 256 characters (omit it to have one generated)")
	}
	return nil
}

// visible loads a webhook the caller may see.
func (s *Webhooks) visible(ctx context.Context, sc Scope, id string) (store.Webhook, error) {
	w, err := s.Store.GetWebhook(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !sc.Admin && w.InstanceID != sc.InstanceID) ||
		(err == nil && w.Owner == store.OwnerOperator && !sc.Operator) { // the operator's are invisible to others
		return w, webhookNotFound()
	}
	return w, err
}

func (s *Webhooks) reload(ctx context.Context) {
	if s.Dispatcher != nil {
		_ = s.Dispatcher.Reload(context.WithoutCancel(ctx))
	}
}

func (s *Webhooks) Create(ctx context.Context, sc Scope, in WebhookInput) (WebhookView, error) {
	w := store.Webhook{ID: secure.NewID("wh_"), Version: events.Version, Enabled: true}
	if sc.Operator {
		w.Owner = store.OwnerOperator
	}
	if in.URL == nil {
		return WebhookView{}, apierr.Validation("url is required")
	}
	if err := validateURL(*in.URL); err != nil {
		return WebhookView{}, err
	}
	w.URL = *in.URL
	evs, err := validateEvents(in.Events)
	if err != nil {
		return WebhookView{}, err
	}
	w.Events = evs
	if w.FiltersJSON, err = validateFilters(in.Filters); err != nil {
		return WebhookView{}, err
	}
	if in.Version != nil && *in.Version != events.Version {
		return WebhookView{}, apierr.Validation("unsupported version (supported: 1)")
	}
	if in.Enabled != nil {
		w.Enabled = *in.Enabled
	}
	if in.InstanceID != nil {
		w.InstanceID = *in.InstanceID
	}
	if !sc.Admin {
		if w.InstanceID != "" && w.InstanceID != sc.InstanceID {
			return WebhookView{}, apierr.Forbidden()
		}
		w.InstanceID = sc.InstanceID // an instance token only registers for its own instance
	} else if w.InstanceID != "" {
		if _, err := s.Store.GetInstance(ctx, w.InstanceID); err != nil {
			return WebhookView{}, mapErr(err)
		}
	}
	w.Secret = webhook.NewSecret()
	if in.Secret != nil {
		if err := validateSecret(*in.Secret); err != nil {
			return WebhookView{}, err
		}
		w.Secret = *in.Secret
	}
	if err := s.Store.CreateWebhook(ctx, w); err != nil {
		return WebhookView{}, mapErr(err)
	}
	s.reload(ctx)
	created, err := s.Store.GetWebhook(ctx, w.ID)
	if err != nil {
		return WebhookView{}, err
	}
	v := webhookView(created)
	v.Secret = w.Secret
	return v, nil
}

// List returns the caller's webhooks; the admin may filter by instance ("" = all).
func (s *Webhooks) List(ctx context.Context, sc Scope, instanceID string) ([]WebhookView, error) {
	list, err := s.Store.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	if !sc.Admin {
		instanceID = sc.InstanceID
	}
	out := []WebhookView{}
	for _, w := range list {
		if (instanceID != "" && w.InstanceID != instanceID) || (w.Owner == store.OwnerOperator && !sc.Operator) {
			continue
		}
		out = append(out, webhookView(w))
	}
	return out, nil
}

func (s *Webhooks) Get(ctx context.Context, sc Scope, id string) (WebhookView, error) {
	w, err := s.visible(ctx, sc, id)
	if err != nil {
		return WebhookView{}, err
	}
	return webhookView(w), nil
}

func (s *Webhooks) Update(ctx context.Context, sc Scope, id string, in WebhookInput) (WebhookView, error) {
	w, err := s.visible(ctx, sc, id)
	if err != nil {
		return WebhookView{}, err
	}
	if in.InstanceID != nil && *in.InstanceID != w.InstanceID {
		return WebhookView{}, apierr.Validation("instance_id cannot be changed; create another webhook")
	}
	if in.Version != nil && *in.Version != events.Version {
		return WebhookView{}, apierr.Validation("unsupported version (supported: 1)")
	}
	if in.URL != nil {
		if err := validateURL(*in.URL); err != nil {
			return WebhookView{}, err
		}
		w.URL = *in.URL
	}
	if in.Events != nil {
		if w.Events, err = validateEvents(in.Events); err != nil {
			return WebhookView{}, err
		}
	}
	if in.Enabled != nil {
		w.Enabled = *in.Enabled
	}
	if in.Filters != nil {
		if w.FiltersJSON, err = validateFilters(in.Filters); err != nil {
			return WebhookView{}, err
		}
	}
	newSecret := ""
	if in.Secret != nil {
		newSecret = *in.Secret
		if newSecret == "" {
			newSecret = webhook.NewSecret() // "secret": "" rotates to a generated one
		} else if err := validateSecret(newSecret); err != nil {
			return WebhookView{}, err
		}
		w.Secret = newSecret
	}
	if err := s.Store.UpdateWebhook(ctx, w); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return WebhookView{}, webhookNotFound()
		}
		return WebhookView{}, err
	}
	s.reload(ctx)
	updated, err := s.Store.GetWebhook(ctx, id)
	if err != nil {
		return WebhookView{}, err
	}
	v := webhookView(updated)
	v.Secret = newSecret
	return v, nil
}

func (s *Webhooks) Delete(ctx context.Context, sc Scope, id string) error {
	if _, err := s.visible(ctx, sc, id); err != nil {
		return err
	}
	if err := s.Store.DeleteWebhook(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return webhookNotFound()
		}
		return err
	}
	s.reload(ctx)
	return nil
}

// Deliveries lists a webhook's deliveries, newest first (status: pending | delivered |
// failed, or "" for all).
func (s *Webhooks) Deliveries(ctx context.Context, sc Scope, id, status string, limit int, cursor string) (DeliveryPage, error) {
	if _, err := s.visible(ctx, sc, id); err != nil {
		return DeliveryPage{}, err
	}
	switch status {
	case "", store.DeliveryPending, store.DeliveryDelivered, store.DeliveryFailed:
	default:
		return DeliveryPage{}, apierr.Validation("status must be pending, delivered or failed")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var cur int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n <= 0 {
			return DeliveryPage{}, apierr.Validation("invalid cursor")
		}
		cur = n
	}
	ds, next, err := s.Store.ListDeliveries(ctx, id, status, limit, cur)
	if err != nil {
		return DeliveryPage{}, err
	}
	page := DeliveryPage{Deliveries: make([]DeliveryView, 0, len(ds))}
	for _, d := range ds {
		page.Deliveries = append(page.Deliveries, deliveryView(d))
	}
	if next > 0 {
		page.NextCursor = strconv.FormatInt(next, 10)
	}
	return page, nil
}

// Retry puts a dead-lettered delivery back in the queue.
func (s *Webhooks) Retry(ctx context.Context, sc Scope, id, deliveryID string) (DeliveryView, error) {
	if _, err := s.visible(ctx, sc, id); err != nil {
		return DeliveryView{}, err
	}
	d, err := s.Store.GetDelivery(ctx, id, deliveryID)
	if errors.Is(err, store.ErrNotFound) {
		return DeliveryView{}, notFound("delivery")
	}
	if err != nil {
		return DeliveryView{}, err
	}
	if d.Status != store.DeliveryFailed {
		return DeliveryView{}, apierr.New(http.StatusConflict, "conflict", "only failed deliveries can be retried (status: "+d.Status+")", false)
	}
	if s.Dispatcher == nil {
		return DeliveryView{}, apierr.New(http.StatusServiceUnavailable, "shutting_down", "webhook delivery is not running", true)
	}
	if err := s.Dispatcher.Retry(ctx, id, deliveryID); err != nil {
		if errors.Is(err, store.ErrNotFound) { // changed meanwhile
			return DeliveryView{}, apierr.New(http.StatusConflict, "conflict", "delivery is no longer failed", false)
		}
		return DeliveryView{}, err
	}
	d, err = s.Store.GetDelivery(ctx, id, deliveryID)
	if err != nil {
		return DeliveryView{}, err
	}
	return deliveryView(d), nil
}
