package worker

import (
	"regexp"
	"strings"
)

// detailSink is implemented by sinks that can carry the error behind a failure reason
// (ConnState.detail, marco 11). Sinks without it get the plain state.
type detailSink interface {
	ConnStateDetail(state, reason, jid, detail string)
}

// reportConnDetail reports a state with the error text that caused it.
func reportConnDetail(s Sink, state, reason, jid string, err error) {
	if ds, ok := s.(detailSink); ok && err != nil {
		ds.ConnStateDetail(state, reason, jid, SanitizeDetail(err.Error()))
		return
	}
	s.ConnState(state, reason, jid)
}

var (
	// userinfo in URLs ("socks5://user:pass@host") and bare "user:pass@host".
	userinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)?[^\s/@:]+:[^\s/@]*@`)
	// Long digit runs may be phone numbers (the account JID); tokens never belong here.
	digitsRe = regexp.MustCompile(`\d{6,}`)
	tokenRe  = regexp.MustCompile(`\blz[a-z]_[A-Za-z0-9_-]+`)
)

// maxDetail bounds the detail text.
const maxDetail = 300

// SanitizeDetail removes credentials, phone-like numbers and tokens from an error text
// and bounds its length, so it can go to webhooks.
func SanitizeDetail(msg string) string {
	msg = userinfoRe.ReplaceAllStringFunc(msg, func(m string) string {
		scheme := ""
		if i := strings.Index(m, "://"); i >= 0 {
			scheme = m[:i+3]
		}
		return scheme + "[redacted]@"
	})
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
