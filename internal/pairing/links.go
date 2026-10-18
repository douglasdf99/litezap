// Package pairing serves the hosted pairing page for partners (marco 11, PAR-6): a
// signed, short-lived link that pairs one instance (live QR code or phone code) without
// exposing its token, and the instance.paired event.
package pairing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// MaxTTL is the longest a link lives; DefaultTTL is used when none is asked for.
const (
	MaxTTL     = 10 * time.Minute
	DefaultTTL = 10 * time.Minute
	MinTTL     = time.Minute
	// UsedGrace is how long a spent link still shows "paired" to the page that used it.
	UsedGrace = 2 * time.Minute
	// maxLive bounds the live links of one instance.
	maxLive = 20
)

const tokenPrefix = "pl1"

// Links mints, resolves and revokes pairing links. A link is a signed token
// (HMAC-SHA256 over the link ID and expiry) that names a row in the store; the row lets
// a link be revoked and spent by the first completed pairing.
type Links struct {
	Store *store.Store
	// Key signs the tokens (32 random bytes kept in the data dir).
	Key []byte
	// BaseURL is the public base URL of the server (public_url); the page lives at
	// BaseURL + "/pair/".
	BaseURL string
	Now     func() time.Time
}

// Link is a freshly minted link. Token is the credential; URL carries it in the fragment
// (never sent to the server, so it stays out of access logs and Referer headers).
type Link struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (l *Links) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Links) sign(id string, exp int64) []byte {
	m := hmac.New(sha256.New, l.Key)
	fmt.Fprintf(m, "litezap-pairing-v1|%s|%d", id, exp)
	return m.Sum(nil)
}

// ErrInvalid is returned for any link that cannot be used (bad signature, expired,
// revoked, spent or unknown); the page does not learn which.
var ErrInvalid = apierr.New(http.StatusGone, "link_expired", "pairing link is invalid, expired or already used", false)

// Mint creates a link for the instance valid for ttl (0: DefaultTTL; at most MaxTTL).
func (l *Links) Mint(ctx context.Context, instanceID string, ttl time.Duration) (Link, error) {
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return Link{}, apierr.Validation(fmt.Sprintf("ttl_seconds must be between %d and %d", int(MinTTL.Seconds()), int(MaxTTL.Seconds())))
	}
	if _, err := l.Store.GetInstance(ctx, instanceID); errors.Is(err, store.ErrNotFound) {
		return Link{}, apierr.InstanceNotFound()
	} else if err != nil {
		return Link{}, err
	}
	now := l.now()
	_ = l.Store.PrunePairingLinks(ctx, now.Add(-24*time.Hour))
	if n, err := l.Store.CountLivePairingLinks(ctx, instanceID, now); err != nil {
		return Link{}, err
	} else if n >= maxLive {
		return Link{}, apierr.New(http.StatusConflict, "limit_reached",
			fmt.Sprintf("the instance already has %d live pairing links; revoke them first", maxLive), false)
	}
	raw := make([]byte, 16)
	rand.Read(raw)
	id := base64.RawURLEncoding.EncodeToString(raw)
	exp := now.Add(ttl).Truncate(time.Second)
	if err := l.Store.CreatePairingLink(ctx, store.PairingLink{ID: id, InstanceID: instanceID, CreatedAt: now, ExpiresAt: exp}); err != nil {
		return Link{}, err
	}
	tok := tokenPrefix + "." + id + "." + strconv.FormatInt(exp.Unix(), 10) + "." +
		base64.RawURLEncoding.EncodeToString(l.sign(id, exp.Unix()))
	return Link{ID: id, Token: tok, URL: strings.TrimRight(l.BaseURL, "/") + "/pair/#t=" + tok, ExpiresAt: exp.UTC()}, nil
}

// Resolve checks a token and returns its link. A spent link is returned (the caller
// decides about the grace period); expired, revoked and forged ones are ErrInvalid.
func (l *Links) Resolve(ctx context.Context, tok string) (store.PairingLink, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 4 || parts[0] != tokenPrefix || len(tok) > 200 {
		return store.PairingLink{}, ErrInvalid
	}
	id := parts[1]
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return store.PairingLink{}, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !hmac.Equal(sig, l.sign(id, exp)) {
		return store.PairingLink{}, ErrInvalid
	}
	now := l.now()
	link, err := l.Store.GetPairingLink(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.PairingLink{}, ErrInvalid
	}
	if err != nil {
		return store.PairingLink{}, err
	}
	if link.ExpiresAt.Unix() != exp || !link.RevokedAt.IsZero() {
		return store.PairingLink{}, ErrInvalid
	}
	if link.UsedAt.IsZero() && !now.Before(link.ExpiresAt) {
		return store.PairingLink{}, ErrInvalid
	}
	if !link.UsedAt.IsZero() && now.Sub(link.UsedAt) > UsedGrace {
		return store.PairingLink{}, ErrInvalid
	}
	return link, nil
}

// Revoke revokes every link of the instance that is not spent yet.
func (l *Links) Revoke(ctx context.Context, instanceID string) (int, error) {
	if _, err := l.Store.GetInstance(ctx, instanceID); errors.Is(err, store.ErrNotFound) {
		return 0, apierr.InstanceNotFound()
	} else if err != nil {
		return 0, err
	}
	return l.Store.RevokePairingLinks(ctx, instanceID, l.now())
}
