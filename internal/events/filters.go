package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Filters narrow the events of a webhook beyond its types (Z-API "filtros de webhook").
// Each filter only applies to events that carry the field: instance.* events have no
// chat and always pass; message_types only concerns message events (received, sent,
// reaction, button_reply). The zero value lets everything through.
type Filters struct {
	// Chat keeps only direct chats ("direct") or only groups ("group").
	Chat string `json:"chat,omitempty"`
	// Chats, when set, is the allowlist of chat JIDs; ExcludeChats is a denylist.
	Chats        []string `json:"chats,omitempty"`
	ExcludeChats []string `json:"exclude_chats,omitempty"`
	// MessageTypes keeps only message events of these types (text, image, ...).
	MessageTypes []string `json:"message_types,omitempty"`
	// FromMe: true keeps only the instance's own messages, false drops them.
	FromMe *bool `json:"from_me,omitempty"`
}

// MessageTypes a filter may name (the types of received and sent messages).
var MessageTypes = []string{"text", "image", "video", "gif", "ptv", "audio", "document", "sticker",
	"location", "contact", "poll", "reaction", "button_reply", "unknown"}

const maxFilterChats = 500

// Empty reports whether f lets everything through.
func (f Filters) Empty() bool {
	return f.Chat == "" && len(f.Chats) == 0 && len(f.ExcludeChats) == 0 && len(f.MessageTypes) == 0 && f.FromMe == nil
}

// NormalizeChat turns a phone number (digits, "+", spaces, dashes) into its user JID;
// JIDs are kept (without a device part). "" for an invalid value.
func NormalizeChat(v string) string {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "@") {
		user, server, _ := strings.Cut(v, "@")
		if user == "" || server == "" {
			return ""
		}
		if i := strings.IndexByte(user, ':'); i >= 0 && server != "g.us" {
			user = user[:i]
		}
		return user + "@" + server
	}
	digits := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
			return -1
		}
		return 'x'
	}, v)
	if len(digits) < 8 || len(digits) > 15 || strings.Contains(digits, "x") {
		return ""
	}
	return digits + "@s.whatsapp.net"
}

// Normalize validates f and returns it with chats normalized and duplicates removed.
func (f Filters) Normalize() (Filters, error) {
	switch f.Chat {
	case "", "direct", "group":
	default:
		return f, errors.New(`filters.chat must be "direct" or "group"`)
	}
	if len(f.Chats)+len(f.ExcludeChats) > maxFilterChats {
		return f, fmt.Errorf("filters accept at most %d chats", maxFilterChats)
	}
	norm := func(name string, in []string) ([]string, error) {
		var out []string
		for _, c := range in {
			n := NormalizeChat(c)
			if n == "" {
				return nil, fmt.Errorf("filters.%s: invalid chat %q (phone number with country code or JID)", name, c)
			}
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out, nil
	}
	var err error
	if f.Chats, err = norm("chats", f.Chats); err != nil {
		return f, err
	}
	if f.ExcludeChats, err = norm("exclude_chats", f.ExcludeChats); err != nil {
		return f, err
	}
	var types []string
	for _, t := range f.MessageTypes {
		t = strings.TrimSpace(t)
		if !slices.Contains(MessageTypes, t) {
			return f, fmt.Errorf("filters.message_types: unknown type %q; valid: %s", t, strings.Join(MessageTypes, ", "))
		}
		if !slices.Contains(types, t) {
			types = append(types, t)
		}
	}
	f.MessageTypes = types
	return f, nil
}

// Match reports whether e passes f.
func (f Filters) Match(e Envelope) bool {
	a := e.Attrs
	if a.Chat != "" {
		switch f.Chat {
		case "direct":
			if isGroupJID(a.Chat) {
				return false
			}
		case "group":
			if !isGroupJID(a.Chat) {
				return false
			}
		}
		if len(f.Chats) > 0 && !slices.Contains(f.Chats, a.Chat) {
			return false
		}
		if slices.Contains(f.ExcludeChats, a.Chat) {
			return false
		}
	}
	if a.MessageType != "" && len(f.MessageTypes) > 0 && !slices.Contains(f.MessageTypes, a.MessageType) {
		return false
	}
	if f.FromMe != nil && a.HasFromMe && a.FromMe != *f.FromMe {
		return false
	}
	return true
}

// ParseFilters decodes stored filters ("" means none).
func ParseFilters(raw string) (Filters, error) {
	var f Filters
	if raw == "" {
		return f, nil
	}
	err := json.Unmarshal([]byte(raw), &f)
	return f, err
}

// JSON encodes f for storage ("" when empty).
func (f Filters) JSON() string {
	if f.Empty() {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}
