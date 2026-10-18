package worker

import (
	"net/url"
	"regexp"
	"strings"
)

// detailSink is implemented by sinks that can carry the error behind a failure reason
// (ConnState.detail, marco 11). Sinks without it get the plain state.
type detailSink interface {
	ConnStateDetail(state, reason, jid, detail string)
}

// reportConnDetail reports a state with the error text that caused it, scrubbed of the
// proxy credentials.
func (w *Whatsmeow) reportConnDetail(state, reason, jid string, err error) {
	if ds, ok := w.sink.(detailSink); ok && err != nil {
		ds.ConnStateDetail(state, reason, jid, SanitizeDetail(err.Error(), w.proxySecrets...))
		return
	}
	w.sink.ConnState(state, reason, jid)
}

// ProxySecrets returns the user name and password of a proxy URL in the forms they may
// appear in an error text (raw and percent-encoded), longest first.
func ProxySecrets(proxyURL string) []string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return nil
	}
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v, url.QueryEscape(v), url.PathEscape(v))
		}
	}
	add(u.User.Username())
	if p, ok := u.User.Password(); ok {
		add(p)
	}
	// Longest first, so a secret containing another is replaced whole.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

var (
	// userinfo in URLs ("socks5://user:pass@host", "socks5://:pass@host"): everything up
	// to the last "@" before the host.
	schemeUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s]*@`)
	// bare "user:pass@host" (the configured proxy's secrets are removed literally first).
	bareUserinfoRe = regexp.MustCompile(`[^\s/:]*:[^\s/]*@`)
	// Long digit runs may be phone numbers (the account JID); tokens never belong here.
	digitsRe = regexp.MustCompile(`\d{6,}`)
	tokenRe  = regexp.MustCompile(`\blz[a-z]_[A-Za-z0-9_-]+`)
)

// maxDetail bounds the detail text.
const maxDetail = 300

// SanitizeDetail removes the given secrets (the proxy credentials), any URL userinfo,
// phone-like numbers and tokens from an error text and bounds its length, so it can go
// to webhooks.
func SanitizeDetail(msg string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 3 { // shorter values would mangle the text and reveal nothing
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	msg = schemeUserinfoRe.ReplaceAllString(msg, "${1}[redacted]@")
	msg = bareUserinfoRe.ReplaceAllString(msg, "[redacted]@")
	msg = digitsRe.ReplaceAllString(msg, "[redacted]")
	msg = tokenRe.ReplaceAllString(msg, "[redacted]")
	if r := []rune(msg); len(r) > maxDetail {
		msg = string(r[:maxDetail]) + "…"
	}
	return msg
}

func (s watchedSink) ConnStateDetail(state, reason, jid, detail string) {
	s.d.observe(state, reason)
	if ds, ok := s.Sink.(detailSink); ok {
		ds.ConnStateDetail(state, reason, jid, detail)
		return
	}
	s.Sink.ConnState(state, reason, jid)
}
