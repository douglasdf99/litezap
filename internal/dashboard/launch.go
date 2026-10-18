package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// LaunchCodeTTL is how long a one-click login code stays valid.
	LaunchCodeTTL = 2 * time.Minute
	// maxPendingCodes bounds the codes waiting to be redeemed; minting more drops the oldest.
	maxPendingCodes = 8
)

// LaunchCodes holds single-use, short-lived login codes minted by a process that already
// has the admin token (the tray launcher). Redeeming a code once hands the browser the
// admin token; it never works twice.
type LaunchCodes struct {
	mu    sync.Mutex
	codes []launchCode
	now   func() time.Time
}

type launchCode struct {
	code    string
	expires time.Time
}

func NewLaunchCodes() *LaunchCodes { return &LaunchCodes{now: time.Now} }

// Mint returns a new code (256 random bits, URL-safe).
func (l *LaunchCodes) Mint() string {
	b := make([]byte, 32)
	rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	if len(l.codes) >= maxPendingCodes {
		l.codes = l.codes[1:]
	}
	l.codes = append(l.codes, launchCode{code: code, expires: l.now().Add(LaunchCodeTTL)})
	return code
}

// Redeem consumes code and reports whether it was valid and unexpired.
func (l *LaunchCodes) Redeem(code string) bool {
	if code == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	found := -1
	// Compare against every pending code in constant time per code.
	for i, c := range l.codes {
		if subtle.ConstantTimeCompare([]byte(c.code), []byte(code)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return false
	}
	l.codes = append(l.codes[:found], l.codes[found+1:]...)
	return true
}

func (l *LaunchCodes) prune() {
	now := l.now()
	kept := l.codes[:0]
	for _, c := range l.codes {
		if now.Before(c.expires) {
			kept = append(kept, c)
		}
	}
	l.codes = kept
}
