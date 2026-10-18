// Package webhook delivers events to registered endpoints (spec 5.3): a persistent
// outbox, HMAC signatures, exponential retry and a dead-letter.
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header names sent with every delivery.
const (
	HeaderSignature = "X-LiteZap-Signature"
	HeaderEventID   = "X-LiteZap-Event-Id"
	HeaderEventType = "X-LiteZap-Event-Type"
)

// NewSecret returns a random signing secret ("whsec_" + 32 random bytes).
func NewSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b)
}

func mac(secret string, ts int64, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(strconv.FormatInt(ts, 10)))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Sign returns the X-LiteZap-Signature value: "t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>".
func Sign(secret string, t time.Time, body []byte) string {
	ts := t.Unix()
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + mac(secret, ts, body)
}

var (
	ErrBadSignature = errors.New("webhook signature does not match")
	ErrStale        = errors.New("webhook signature timestamp outside the tolerance")
)

// Verify checks a signature header as a receiver should: the HMAC must match and t must
// be within tolerance of now (anti-replay; the spec recommends 5 minutes).
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts int64 = -1
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrBadSignature
			}
			ts = n
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts < 0 || len(sigs) == 0 {
		return ErrBadSignature
	}
	want := mac(secret, ts, body)
	ok := false
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			ok = true
		}
	}
	if !ok {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrStale
	}
	return nil
}

// Backoff is the wait after the attempt-th failed attempt (1-based): base doubled each
// time, capped at max (1s, 2s, 4s ... 1h).
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return min(d, max)
}
