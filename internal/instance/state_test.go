package instance

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to State
		want     bool
	}{
		{Created, Starting, true},
		{Created, Stopped, true},
		{Created, Connected, false},
		{Starting, QRPending, true},
		{Starting, Connecting, true},
		{Starting, Connected, true},    // worker snapshot may skip connecting
		{Starting, Reconnecting, true}, // initial connect failed, retrying
		{QRPending, Connecting, true},
		{QRPending, Connected, true},
		{Connecting, Connected, true},
		{Connecting, Reconnecting, true},
		{Connected, Reconnecting, true},
		{Reconnecting, Connected, true},
		{Connected, QRPending, false},
		{Connected, LoggedOut, true},
		{Reconnecting, Banned, true},
		{QRPending, Failed, true},
		{Connected, Starting, true}, // worker crash
		{Connected, Stopped, true},
		{LoggedOut, Starting, true},
		{Failed, Starting, true},
		{Stopped, Starting, true},
		{LoggedOut, Connected, false},
		{Banned, Starting, false},
		{Banned, Stopped, false},
		{Connected, Connected, true}, // no-op
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestPredicates(t *testing.T) {
	for _, s := range []State{Starting, QRPending, Connecting, Connected, Reconnecting} {
		if !s.Active() || s.Terminal() {
			t.Errorf("%s should be active, not terminal", s)
		}
	}
	for _, s := range []State{LoggedOut, Banned, Failed} {
		if s.Active() || !s.Terminal() {
			t.Errorf("%s should be terminal, not active", s)
		}
	}
	if Stopped.Active() || Stopped.Terminal() || Created.Active() {
		t.Error("stopped/created are neither active nor terminal")
	}
}
