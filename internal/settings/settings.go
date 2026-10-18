// Package settings holds the per-instance send settings (spec 4.1 and 4.6): pacing,
// the new-number ramp and the outbound proxy.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

type Ramp struct {
	Enabled     bool `json:"enabled" yaml:"enabled"`
	StartPerDay int  `json:"start_per_day" yaml:"start_per_day"`
	EndPerDay   int  `json:"end_per_day" yaml:"end_per_day"`
	Days        int  `json:"days" yaml:"days"`
}

// Window is the daily send window, "HH:MM" in the instance's timezone; both empty means
// sending at any hour.
type Window struct {
	Start string `json:"start" yaml:"start"`
	End   string `json:"end" yaml:"end"`
}

// Calls is the incoming call policy (marco 7).
type Calls struct {
	// AutoReject rejects every incoming call as soon as it rings.
	AutoReject bool `json:"auto_reject" yaml:"auto_reject"`
	// RejectMessage, when set, is sent to the caller (1:1 calls) after an automatic
	// rejection, through the send queue (pacing, window and ramp apply); at most once per
	// caller every 10 minutes.
	RejectMessage string `json:"reject_message" yaml:"reject_message"`
}

// Presence controls the account's own online status (marco 7).
type Presence struct {
	// Online marks the account "available" while connected. Needed to receive typing
	// events and contacts' online status, but while it is on WhatsApp treats the account
	// as active on this device and the phone stops showing notifications. Off by default.
	Online bool `json:"online" yaml:"online"`
}

// MaxRejectMessage bounds calls.reject_message (bytes).
const MaxRejectMessage = 1000

// Settings are the effective settings of an instance.
type Settings struct {
	MinIntervalMS    int    `json:"min_interval_ms" yaml:"min_interval_ms"`
	JitterMS         int    `json:"jitter_ms" yaml:"jitter_ms"`
	MaxPerMinute     int    `json:"max_per_minute" yaml:"max_per_minute"`
	TypingSimulation bool   `json:"typing_simulation" yaml:"typing_simulation"`
	Ramp             Ramp   `json:"ramp" yaml:"ramp"`
	SendWindow       Window `json:"send_window" yaml:"send_window"`
	// Timezone is an IANA name ("America/Sao_Paulo"); empty means the server's local time.
	// It defines the send window and the ramp's day.
	Timezone string `json:"timezone" yaml:"timezone"`
	// UnsafePacing lifts the pacing floors (min_interval_ms >= 1000, max_per_minute <= 60).
	// Only an admin can set it.
	UnsafePacing bool `json:"unsafe_pacing" yaml:"unsafe_pacing"`
	// ProxyURL is never read from the instance_defaults config block: the global
	// default proxy is config.DefaultProxyURL.
	ProxyURL string   `json:"proxy_url" yaml:"-"`
	Calls    Calls    `json:"calls" yaml:"calls"`
	Presence Presence `json:"presence" yaml:"presence"`
}

// Defaults follows spec 4.1: 3s + 0-2s jitter, 20/min, typing on, ramp 50 -> 150 over 14
// days, sending only between 08:00 and 20:00 server time.
func Defaults() Settings {
	return Settings{
		MinIntervalMS: 3000, JitterMS: 2000, MaxPerMinute: 20, TypingSimulation: true,
		Ramp:       Ramp{Enabled: true, StartPerDay: 50, EndPerDay: 150, Days: 14},
		SendWindow: Window{Start: "08:00", End: "20:00"},
	}
}

func (s Settings) MinInterval() time.Duration {
	return time.Duration(s.MinIntervalMS) * time.Millisecond
}
func (s Settings) Jitter() time.Duration { return time.Duration(s.JitterMS) * time.Millisecond }

func (s Settings) Validate() error {
	switch {
	case s.MinIntervalMS < 0 || s.MinIntervalMS > 600_000:
		return errors.New("min_interval_ms must be between 0 and 600000")
	case s.JitterMS < 0 || s.JitterMS > 600_000:
		return errors.New("jitter_ms must be between 0 and 600000")
	case s.MaxPerMinute < 1 || s.MaxPerMinute > 600:
		return errors.New("max_per_minute must be between 1 and 600")
	case s.Ramp.StartPerDay < 1 || s.Ramp.EndPerDay < s.Ramp.StartPerDay:
		return errors.New("ramp needs 1 <= start_per_day <= end_per_day")
	case s.Ramp.Days < 1 || s.Ramp.Days > 365:
		return errors.New("ramp.days must be between 1 and 365")
	case !s.UnsafePacing && s.MinIntervalMS < 1000:
		return errors.New("min_interval_ms must be at least 1000 (unsafe_pacing lifts this, admin only)")
	case !s.UnsafePacing && s.MaxPerMinute > 60:
		return errors.New("max_per_minute must be at most 60 (unsafe_pacing lifts this, admin only)")
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("timezone %q is not a valid IANA zone", s.Timezone)
		}
	}
	if err := s.SendWindow.validate(); err != nil {
		return err
	}
	if len(s.Calls.RejectMessage) > MaxRejectMessage {
		return fmt.Errorf("calls.reject_message is limited to %d bytes", MaxRejectMessage)
	}
	return ValidateProxyURL(s.ProxyURL)
}

func parseClock(v string) (int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, fmt.Errorf("send_window times must be HH:MM, got %q", v)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (w Window) validate() error {
	if w.Start == "" && w.End == "" {
		return nil
	}
	start, err := parseClock(w.Start)
	if err != nil {
		return err
	}
	end, err := parseClock(w.End)
	if err != nil {
		return err
	}
	if end <= start {
		return errors.New("send_window.end must be after send_window.start (same day)")
	}
	return nil
}

// Location is the instance's timezone (the server's local zone when unset or invalid).
func (s Settings) Location() *time.Location {
	if s.Timezone != "" {
		if loc, err := time.LoadLocation(s.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// DayStart is midnight of t's day in the instance's timezone (the ramp's day).
func (s Settings) DayStart(t time.Time) time.Time {
	y, m, d := t.In(s.Location()).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.Location())
}

// WindowAt returns the send window containing t or, when t is outside it, the next one.
// With no window configured, start is zero and open is always true.
func (s Settings) WindowAt(t time.Time) (start, end time.Time, open bool) {
	if s.SendWindow.Start == "" && s.SendWindow.End == "" {
		return time.Time{}, time.Time{}, true
	}
	sm, err1 := parseClock(s.SendWindow.Start)
	em, err2 := parseClock(s.SendWindow.End)
	if err1 != nil || err2 != nil || em <= sm {
		return time.Time{}, time.Time{}, true
	}
	day := s.DayStart(t)
	at := func(d time.Time, min int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), min/60, min%60, 0, 0, d.Location())
	}
	start, end = at(day, sm), at(day, em)
	switch {
	case t.Before(start):
		return start, end, false
	case t.Before(end):
		return start, end, true
	}
	next := day.AddDate(0, 0, 1)
	return at(next, sm), at(next, em), false
}

// ValidateProxyURL accepts "" (no proxy) or an http, https or socks5 URL with a host.
func ValidateProxyURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("proxy_url is not a valid URL")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("proxy_url scheme must be http, https or socks5, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("proxy_url needs a host")
	}
	return nil
}

// RedactProxy hides the password of a proxy URL for display.
func RedactProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}

// RampPatch and Patch are partial updates: nil fields keep their current value.
type RampPatch struct {
	Enabled     *bool `json:"enabled,omitempty"`
	StartPerDay *int  `json:"start_per_day,omitempty"`
	EndPerDay   *int  `json:"end_per_day,omitempty"`
	Days        *int  `json:"days,omitempty"`
}

// CallsPatch and PresencePatch are partial updates of Calls and Presence.
type CallsPatch struct {
	AutoReject    *bool   `json:"auto_reject,omitempty"`
	RejectMessage *string `json:"reject_message,omitempty"`
}

type PresencePatch struct {
	Online *bool `json:"online,omitempty"`
}

// Patch is both the PUT body and what is stored per instance (instances.settings_json):
// only overridden fields are kept, so instances follow later changes of the defaults.
type Patch struct {
	MinIntervalMS    *int       `json:"min_interval_ms,omitempty"`
	JitterMS         *int       `json:"jitter_ms,omitempty"`
	MaxPerMinute     *int       `json:"max_per_minute,omitempty"`
	TypingSimulation *bool      `json:"typing_simulation,omitempty"`
	Ramp             *RampPatch `json:"ramp,omitempty"`
	SendWindow       *Window    `json:"send_window,omitempty"`
	Timezone         *string    `json:"timezone,omitempty"`
	UnsafePacing     *bool      `json:"unsafe_pacing,omitempty"` // admin only
	// ProxyURL "" clears the override (the instance falls back to the default proxy). Admin only.
	ProxyURL *string        `json:"proxy_url,omitempty"`
	Calls    *CallsPatch    `json:"calls,omitempty"`
	Presence *PresencePatch `json:"presence,omitempty"`
}

// Merge returns p with every field set in q overriding it.
func (p Patch) Merge(q Patch) Patch {
	pick := func(a, b *int) *int {
		if b != nil {
			return b
		}
		return a
	}
	pickB := func(a, b *bool) *bool {
		if b != nil {
			return b
		}
		return a
	}
	out := Patch{
		MinIntervalMS:    pick(p.MinIntervalMS, q.MinIntervalMS),
		JitterMS:         pick(p.JitterMS, q.JitterMS),
		MaxPerMinute:     pick(p.MaxPerMinute, q.MaxPerMinute),
		TypingSimulation: pickB(p.TypingSimulation, q.TypingSimulation),
		UnsafePacing:     pickB(p.UnsafePacing, q.UnsafePacing),
		SendWindow:       p.SendWindow,
		Timezone:         p.Timezone,
		ProxyURL:         p.ProxyURL,
	}
	if q.SendWindow != nil {
		out.SendWindow = q.SendWindow
	}
	if q.Timezone != nil {
		out.Timezone = q.Timezone
	}
	if q.ProxyURL != nil {
		out.ProxyURL = q.ProxyURL
	}
	if p.Calls != nil || q.Calls != nil {
		var a, b CallsPatch
		if p.Calls != nil {
			a = *p.Calls
		}
		if q.Calls != nil {
			b = *q.Calls
		}
		out.Calls = &CallsPatch{AutoReject: pickB(a.AutoReject, b.AutoReject), RejectMessage: a.RejectMessage}
		if b.RejectMessage != nil {
			out.Calls.RejectMessage = b.RejectMessage
		}
	}
	if p.Presence != nil || q.Presence != nil {
		var a, b PresencePatch
		if p.Presence != nil {
			a = *p.Presence
		}
		if q.Presence != nil {
			b = *q.Presence
		}
		out.Presence = &PresencePatch{Online: pickB(a.Online, b.Online)}
	}
	if p.Ramp != nil || q.Ramp != nil {
		var a, b RampPatch
		if p.Ramp != nil {
			a = *p.Ramp
		}
		if q.Ramp != nil {
			b = *q.Ramp
		}
		out.Ramp = &RampPatch{
			Enabled:     pickB(a.Enabled, b.Enabled),
			StartPerDay: pick(a.StartPerDay, b.StartPerDay),
			EndPerDay:   pick(a.EndPerDay, b.EndPerDay),
			Days:        pick(a.Days, b.Days),
		}
	}
	return out
}

// Apply returns s with the overrides of p.
func (s Settings) Apply(p Patch) Settings {
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setI(&s.MinIntervalMS, p.MinIntervalMS)
	setI(&s.JitterMS, p.JitterMS)
	setI(&s.MaxPerMinute, p.MaxPerMinute)
	if p.TypingSimulation != nil {
		s.TypingSimulation = *p.TypingSimulation
	}
	if p.UnsafePacing != nil {
		s.UnsafePacing = *p.UnsafePacing
	}
	if p.SendWindow != nil {
		s.SendWindow = *p.SendWindow
	}
	if p.Timezone != nil {
		s.Timezone = *p.Timezone
	}
	if r := p.Ramp; r != nil {
		if r.Enabled != nil {
			s.Ramp.Enabled = *r.Enabled
		}
		setI(&s.Ramp.StartPerDay, r.StartPerDay)
		setI(&s.Ramp.EndPerDay, r.EndPerDay)
		setI(&s.Ramp.Days, r.Days)
	}
	if p.ProxyURL != nil && *p.ProxyURL != "" {
		s.ProxyURL = *p.ProxyURL
	}
	if c := p.Calls; c != nil {
		if c.AutoReject != nil {
			s.Calls.AutoReject = *c.AutoReject
		}
		if c.RejectMessage != nil {
			s.Calls.RejectMessage = *c.RejectMessage
		}
	}
	if p.Presence != nil && p.Presence.Online != nil {
		s.Presence.Online = *p.Presence.Online
	}
	return s
}

// ParsePatch decodes a stored overrides document ("" and "{}" mean no overrides).
func ParsePatch(raw string) (Patch, error) {
	var p Patch
	if raw == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("settings json: %w", err)
	}
	return p, nil
}

// Effective resolves an instance's settings: defaults, the global default proxy, then
// the instance's stored overrides.
func Effective(defaults Settings, defaultProxy, overrides string) (Settings, error) {
	p, err := ParsePatch(overrides)
	if err != nil {
		return Settings{}, err
	}
	base := defaults
	base.ProxyURL = defaultProxy
	return base.Apply(p), nil
}

// DailyCap is the ramp limit of new recipients for the day containing now, or -1 when
// the ramp is off. It grows linearly from StartPerDay (day 0) to EndPerDay (day Days).
func (r Ramp) DailyCap(firstConnected, now time.Time) int {
	if !r.Enabled {
		return -1
	}
	if firstConnected.IsZero() || now.Before(firstConnected) {
		return r.StartPerDay
	}
	days := int(now.Sub(firstConnected) / (24 * time.Hour))
	if days >= r.Days {
		return r.EndPerDay
	}
	return r.StartPerDay + (r.EndPerDay-r.StartPerDay)*days/r.Days
}
