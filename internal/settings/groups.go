package settings

import "fmt"

// Groups bounds the group actions that look like spam to WhatsApp (marco 8): adding
// participants, creating groups and joining by invite link. Zero values mean the
// defaults below.
type Groups struct {
	MaxAddsPerHour   int `json:"max_adds_per_hour" yaml:"max_adds_per_hour"`
	MaxAddsPerDay    int `json:"max_adds_per_day" yaml:"max_adds_per_day"`
	MaxCreatesPerDay int `json:"max_creates_per_day" yaml:"max_creates_per_day"`
	MaxJoinsPerDay   int `json:"max_joins_per_day" yaml:"max_joins_per_day"`
}

// Group limits: defaults and the ceilings that only unsafe_pacing (admin) lifts.
const (
	DefaultAddsPerHour   = 10
	DefaultAddsPerDay    = 50
	DefaultCreatesPerDay = 3
	DefaultJoinsPerDay   = 10

	MaxAddsPerHour   = 30
	MaxAddsPerDay    = 200
	MaxCreatesPerDay = 10
	MaxJoinsPerDay   = 30
)

// Limits returns g with the defaults filled in.
func (g Groups) Limits() Groups {
	or := func(v, def int) int {
		if v == 0 {
			return def
		}
		return v
	}
	return Groups{
		MaxAddsPerHour: or(g.MaxAddsPerHour, DefaultAddsPerHour), MaxAddsPerDay: or(g.MaxAddsPerDay, DefaultAddsPerDay),
		MaxCreatesPerDay: or(g.MaxCreatesPerDay, DefaultCreatesPerDay), MaxJoinsPerDay: or(g.MaxJoinsPerDay, DefaultJoinsPerDay),
	}
}

func (g Groups) validate(unsafe bool) error {
	l := g.Limits()
	for _, c := range []struct {
		name     string
		v, limit int
	}{
		{"groups.max_adds_per_hour", l.MaxAddsPerHour, MaxAddsPerHour},
		{"groups.max_adds_per_day", l.MaxAddsPerDay, MaxAddsPerDay},
		{"groups.max_creates_per_day", l.MaxCreatesPerDay, MaxCreatesPerDay},
		{"groups.max_joins_per_day", l.MaxJoinsPerDay, MaxJoinsPerDay},
	} {
		if c.v < 1 || c.v > 10*c.limit {
			return fmt.Errorf("%s must be between 1 and %d", c.name, 10*c.limit)
		}
		if !unsafe && c.v > c.limit {
			return fmt.Errorf("%s must be at most %d (unsafe_pacing lifts this, admin only)", c.name, c.limit)
		}
	}
	if l.MaxAddsPerHour > l.MaxAddsPerDay {
		return fmt.Errorf("groups.max_adds_per_hour cannot exceed groups.max_adds_per_day")
	}
	return nil
}

// GroupsPatch is a partial update of Groups.
type GroupsPatch struct {
	MaxAddsPerHour   *int `json:"max_adds_per_hour,omitempty"`
	MaxAddsPerDay    *int `json:"max_adds_per_day,omitempty"`
	MaxCreatesPerDay *int `json:"max_creates_per_day,omitempty"`
	MaxJoinsPerDay   *int `json:"max_joins_per_day,omitempty"`
}

func mergeGroups(p, q *GroupsPatch) *GroupsPatch {
	if p == nil && q == nil {
		return nil
	}
	var a, b GroupsPatch
	if p != nil {
		a = *p
	}
	if q != nil {
		b = *q
	}
	pick := func(x, y *int) *int {
		if y != nil {
			return y
		}
		return x
	}
	return &GroupsPatch{
		MaxAddsPerHour: pick(a.MaxAddsPerHour, b.MaxAddsPerHour), MaxAddsPerDay: pick(a.MaxAddsPerDay, b.MaxAddsPerDay),
		MaxCreatesPerDay: pick(a.MaxCreatesPerDay, b.MaxCreatesPerDay), MaxJoinsPerDay: pick(a.MaxJoinsPerDay, b.MaxJoinsPerDay),
	}
}

func (g Groups) apply(p *GroupsPatch) Groups {
	if p == nil {
		return g
	}
	for _, f := range []struct {
		dst *int
		v   *int
	}{
		{&g.MaxAddsPerHour, p.MaxAddsPerHour}, {&g.MaxAddsPerDay, p.MaxAddsPerDay},
		{&g.MaxCreatesPerDay, p.MaxCreatesPerDay}, {&g.MaxJoinsPerDay, p.MaxJoinsPerDay},
	} {
		if f.v != nil {
			*f.dst = *f.v
		}
	}
	return g
}
