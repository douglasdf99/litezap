// Package agenthook implements the agent hook (spec 4.3): an external AI attendant that
// receives each eligible incoming message as a signed POST and answers with a reply and
// actions. Replies go through the normal send queue (pacing, window, ramp, operator
// suspension); failures never send anything (fail-closed).
package agenthook

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration encoded as a Go duration string ("30s", "10m"); a JSON
// number is read as seconds.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("invalid duration %q (use e.g. \"30s\", \"10m\")", s)
		}
		*d = Duration(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("duration must be a string like \"30s\" or a number of seconds")
	}
	*d = Duration(time.Duration(n * float64(time.Second)))
	return nil
}

// Config is an instance's agent hook configuration (stored as JSON, secret included).
type Config struct {
	// Target is where messages go: "url" (default; the signed POST to URL) or "builtin"
	// (the built-in attendant running inside the engine, spec 4.7).
	Target  string `json:"target,omitempty"`
	URL     string `json:"url"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"` // default for chats without an override
	// Timeout of one hook call (default 30s).
	Timeout Duration `json:"timeout"`
	// ContextMessages is how many recent chat messages go in "context" (default 20).
	ContextMessages int  `json:"context_messages"`
	GroupsEnabled   bool `json:"groups_enabled"`
	// HumanPause pauses the hook in a chat after a human answers from the phone (default
	// 30m; 0 disables the pause).
	HumanPause Duration `json:"human_pause"`
	// PauseOnAPISend also pauses on messages sent through the API that are not the hook's
	// own replies (a human answering from an inbox or CRM on top of LiteZap). Off by
	// default: automated API sends would pause the agent too.
	PauseOnAPISend bool `json:"pause_on_api_send"`
	// Ignore lists chat (or sender) JIDs the hook never handles.
	Ignore []string `json:"ignore"`
	// Loop guard: at most MaxRepliesPerWindow replies per chat in ReplyWindow; beyond that
	// the chat is paused (two bots answering each other, a runaway agent).
	MaxRepliesPerWindow int      `json:"max_replies_per_window"`
	ReplyWindow         Duration `json:"reply_window"`
}

// Targets of the hook.
const (
	TargetURL     = "url"
	TargetBuiltin = "builtin"
)

// Builtin reports whether the hook uses the built-in attendant.
func (c Config) Builtin() bool { return c.Target == TargetBuiltin }

// Defaults (spec 4.3, plus the loop guard).
const (
	DefaultTimeout         = 30 * time.Second
	DefaultContextMessages = 20
	DefaultHumanPause      = 30 * time.Minute
	DefaultMaxReplies      = 10
	DefaultReplyWindow     = 10 * time.Minute
)

// DefaultConfig is the starting point of a new hook (no URL, disabled).
func DefaultConfig() Config {
	return Config{Timeout: Duration(DefaultTimeout), ContextMessages: DefaultContextMessages,
		HumanPause: Duration(DefaultHumanPause), Ignore: []string{},
		MaxRepliesPerWindow: DefaultMaxReplies, ReplyWindow: Duration(DefaultReplyWindow)}
}

// Patch changes some fields of a Config (nil = keep).
type Patch struct {
	Target              *string   `json:"target"`
	URL                 *string   `json:"url"`
	Secret              *string   `json:"secret"`
	Enabled             *bool     `json:"enabled"`
	Timeout             *Duration `json:"timeout"`
	ContextMessages     *int      `json:"context_messages"`
	GroupsEnabled       *bool     `json:"groups_enabled"`
	HumanPause          *Duration `json:"human_pause"`
	PauseOnAPISend      *bool     `json:"pause_on_api_send"`
	Ignore              *[]string `json:"ignore"`
	MaxRepliesPerWindow *int      `json:"max_replies_per_window"`
	ReplyWindow         *Duration `json:"reply_window"`
}

// Apply returns c with the patch applied (not validated).
func (p Patch) Apply(c Config) Config {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&c.URL, p.URL)
	set(&c.Target, p.Target)
	if c.Target == TargetURL {
		c.Target = "" // the default is stored as ""
	}
	set(&c.Secret, p.Secret)
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.Timeout != nil {
		c.Timeout = *p.Timeout
	}
	if p.ContextMessages != nil {
		c.ContextMessages = *p.ContextMessages
	}
	if p.GroupsEnabled != nil {
		c.GroupsEnabled = *p.GroupsEnabled
	}
	if p.HumanPause != nil {
		c.HumanPause = *p.HumanPause
	}
	if p.PauseOnAPISend != nil {
		c.PauseOnAPISend = *p.PauseOnAPISend
	}
	if p.Ignore != nil {
		c.Ignore = append([]string{}, (*p.Ignore)...)
	}
	if p.MaxRepliesPerWindow != nil {
		c.MaxRepliesPerWindow = *p.MaxRepliesPerWindow
	}
	if p.ReplyWindow != nil {
		c.ReplyWindow = *p.ReplyWindow
	}
	return c
}

const (
	maxURLLen    = 2048
	maxIgnore    = 1000
	minSecretLen = 16
	maxSecretLen = 256
)

// Validate checks c and normalizes its ignore list with norm (JID normalization).
func (c *Config) Validate(norm func(string) (string, error)) error {
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(c.URL) > maxURLLen {
			return errors.New("url must be an absolute http(s) URL of at most 2048 characters")
		}
	}
	switch {
	case c.Target != "" && c.Target != TargetURL && c.Target != TargetBuiltin:
		return errors.New(`target must be "url" or "builtin"`)
	case c.Enabled && c.URL == "" && c.Target != TargetBuiltin:
		return errors.New("url is required to enable the hook")
	case len(c.Secret) < minSecretLen || len(c.Secret) > maxSecretLen:
		return fmt.Errorf("secret must have %d to %d characters", minSecretLen, maxSecretLen)
	case c.Timeout.D() < time.Second || c.Timeout.D() > 2*time.Minute:
		return errors.New("timeout must be between 1s and 2m")
	case c.ContextMessages < 0 || c.ContextMessages > 100:
		return errors.New("context_messages must be between 0 and 100")
	case c.HumanPause.D() < 0 || c.HumanPause.D() > 7*24*time.Hour:
		return errors.New("human_pause must be between 0 and 168h")
	case c.MaxRepliesPerWindow < 1 || c.MaxRepliesPerWindow > 1000:
		return errors.New("max_replies_per_window must be between 1 and 1000")
	case c.ReplyWindow.D() < time.Minute || c.ReplyWindow.D() > 24*time.Hour:
		return errors.New("reply_window must be between 1m and 24h")
	case len(c.Ignore) > maxIgnore:
		return fmt.Errorf("ignore accepts at most %d JIDs", maxIgnore)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Ignore))
	for _, raw := range c.Ignore {
		j, err := norm(raw)
		if err != nil {
			return fmt.Errorf("ignore: %q is not a phone number or JID", raw)
		}
		if !seen[j] {
			seen[j] = true
			out = append(out, j)
		}
	}
	c.Ignore = out
	return nil
}

// ignores reports whether the hook skips a chat or sender.
func (c Config) ignores(jids ...string) bool {
	for _, i := range c.Ignore {
		for _, j := range jids {
			if j != "" && i == j {
				return true
			}
		}
	}
	return false
}

// NewSecret returns a random signing secret ("agsec_" + 32 random bytes).
func NewSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "agsec_" + base64.RawURLEncoding.EncodeToString(b)
}

// parseConfig reads a stored config; fields missing from older rows take the defaults.
func parseConfig(js string) (Config, error) {
	c := DefaultConfig()
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		return Config{}, err
	}
	if c.Ignore == nil {
		c.Ignore = []string{}
	}
	return c, nil
}

// itoa avoids importing strconv at call sites.
func itoa(n int) string { return strconv.Itoa(n) }
