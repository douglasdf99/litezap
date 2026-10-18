package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// LaunchCodeTTL is how long a one-click login code stays valid: enough for a browser
	// to start cold, short to narrow the window in which another local user could read
	// it from a process list.
	LaunchCodeTTL = time.Minute
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
	// ttl, max and keepOthers distinguish the tray's codes (NewLaunchCodes) from the
	// operator's (NewRemoteLaunchCodes).
	ttl        time.Duration
	max        int
	keepOthers bool
}

type launchCode struct {
	code    string
	expires time.Time
}

func NewLaunchCodes() *LaunchCodes {
	return &LaunchCodes{now: time.Now, ttl: LaunchCodeTTL, max: maxPendingCodes}
}

// RemoteLaunchCodeTTL is how long a code minted by the operator (cloud E7) stays valid:
// the control plane hands the link to the member's browser right away.
const RemoteLaunchCodeTTL = 2 * time.Minute

// NewRemoteLaunchCodes holds codes minted through the operator API for one-click login
// from a managed-hosting console. They are redeemable from any client address (still
// same-origin JSON only), and redeeming one leaves the other members' codes pending.
func NewRemoteLaunchCodes() *LaunchCodes {
	return &LaunchCodes{now: time.Now, ttl: RemoteLaunchCodeTTL, max: 64, keepOthers: true}
}

// Mint returns a new code (256 random bits, URL-safe).
func (l *LaunchCodes) Mint() string {
	code, _ := l.MintWithExpiry()
	return code
}

// MintWithExpiry returns a new code and when it expires.
func (l *LaunchCodes) MintWithExpiry() (string, time.Time) {
	b := make([]byte, 32)
	rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	if len(l.codes) >= l.max {
		l.codes = l.codes[1:]
	}
	exp := l.now().Add(l.ttl)
	l.codes = append(l.codes, launchCode{code: code, expires: exp})
	return code, exp
}

// Redeem consumes code and reports whether it was valid and unexpired. For the tray's
// codes a successful redeem also drops every other pending code (each "open dashboard"
// mints a new one); remote codes belong to different people and stay pending.
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
	if l.keepOthers {
		l.codes = append(l.codes[:found], l.codes[found+1:]...)
	} else {
		l.codes = l.codes[:0]
	}
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
