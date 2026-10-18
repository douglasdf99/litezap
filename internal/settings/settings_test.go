package settings

import (
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestDefaultsAreValid(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	bad := []func(*Settings){
		func(s *Settings) { s.MinIntervalMS = -1 },
		func(s *Settings) { s.MaxPerMinute = 0 },
		func(s *Settings) { s.Ramp.EndPerDay = s.Ramp.StartPerDay - 1 },
		func(s *Settings) { s.Ramp.Days = 0 },
		func(s *Settings) { s.ProxyURL = "ftp://x:1" },
		func(s *Settings) { s.ProxyURL = "socks5://" },
		func(s *Settings) { s.MinIntervalMS = 500 }, // floor
		func(s *Settings) { s.MaxPerMinute = 61 },   // ceiling
		func(s *Settings) { s.Timezone = "Mars/Olympus" },
		func(s *Settings) { s.SendWindow = Window{Start: "20:00", End: "08:00"} },
		func(s *Settings) { s.SendWindow = Window{Start: "8h", End: "20:00"} },
	}
	for i, f := range bad {
		s := Defaults()
		f(&s)
		if s.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	s := Defaults()
	s.ProxyURL = "socks5://user:pw@10.0.0.1:1080"
	s.Timezone = "America/Sao_Paulo"
	s.SendWindow = Window{}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.UnsafePacing, s.MinIntervalMS, s.MaxPerMinute = true, 0, 600
	if err := s.Validate(); err != nil {
		t.Fatalf("unsafe pacing must lift the floors: %v", err)
	}
}

func TestSendWindowAndDay(t *testing.T) {
	s := Defaults()
	s.Timezone = "America/Sao_Paulo" // UTC-3, no DST
	at := func(h, m int) time.Time { return time.Date(2026, 10, 17, h, m, 0, 0, s.Location()) }
	cases := []struct {
		t     time.Time
		start time.Time
		open  bool
	}{
		{at(7, 59), at(8, 0), false},
		{at(8, 0), at(8, 0), true},
		{at(19, 59), at(8, 0), true},
		{at(20, 0), at(8, 0).AddDate(0, 0, 1), false},
		{at(23, 30), at(8, 0).AddDate(0, 0, 1), false},
	}
	for _, c := range cases {
		start, _, open := s.WindowAt(c.t)
		if !start.Equal(c.start) || open != c.open {
			t.Errorf("WindowAt(%v) = %v %v, want %v %v", c.t, start, open, c.start, c.open)
		}
	}
	// 01:00 UTC on the 18th is still the 17th in Sao Paulo.
	if got := s.DayStart(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)); !got.Equal(at(0, 0)) {
		t.Fatalf("day start = %v", got)
	}
	s.SendWindow = Window{}
	if start, _, open := s.WindowAt(at(3, 0)); !start.IsZero() || !open {
		t.Fatal("no window means always open")
	}
}

func TestMergeApplyAndEffective(t *testing.T) {
	stored := Patch{MinIntervalMS: ptr(5000), Ramp: &RampPatch{Enabled: ptr(false)}}
	merged := stored.Merge(Patch{JitterMS: ptr(0), Ramp: &RampPatch{Days: ptr(7)}, ProxyURL: ptr("http://p:3128"),
		SendWindow: &Window{Start: "09:00", End: "18:00"}, Timezone: ptr("UTC")})
	s := Defaults().Apply(merged)
	if s.MinIntervalMS != 5000 || s.JitterMS != 0 || s.MaxPerMinute != 20 {
		t.Fatalf("pacing = %+v", s)
	}
	if s.Ramp.Enabled || s.Ramp.Days != 7 || s.Ramp.StartPerDay != 50 {
		t.Fatalf("ramp = %+v", s.Ramp)
	}
	if s.ProxyURL != "http://p:3128" || s.SendWindow.Start != "09:00" || s.Timezone != "UTC" {
		t.Fatalf("proxy/window/timezone = %q %+v %q", s.ProxyURL, s.SendWindow, s.Timezone)
	}

	eff, err := Effective(Defaults(), "socks5://d:1080", `{"min_interval_ms":1000}`)
	if err != nil || eff.MinIntervalMS != 1000 || eff.ProxyURL != "socks5://d:1080" {
		t.Fatalf("effective = %+v, %v", eff, err)
	}
	// An empty proxy override falls back to the default proxy.
	eff, _ = Effective(Defaults(), "socks5://d:1080", `{"proxy_url":""}`)
	if eff.ProxyURL != "socks5://d:1080" {
		t.Fatalf("proxy = %q", eff.ProxyURL)
	}
	if _, err := Effective(Defaults(), "", "{nope"); err == nil {
		t.Fatal("expected json error")
	}
}

func TestRedactProxy(t *testing.T) {
	got := RedactProxy("http://bob:secret@proxy:3128")
	if strings.Contains(got, "secret") || !strings.Contains(got, "bob") {
		t.Fatalf("got %q", got)
	}
	if RedactProxy("") != "" {
		t.Fatal("empty must stay empty")
	}
}

func TestRampDailyCap(t *testing.T) {
	r := Defaults().Ramp
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want int
	}{
		{t0, 50},
		{t0.Add(24 * time.Hour), 57}, // 50 + 100*1/14
		{t0.Add(7 * 24 * time.Hour), 100},
		{t0.Add(14 * 24 * time.Hour), 150},
		{t0.Add(40 * 24 * time.Hour), 150},
	}
	for _, c := range cases {
		if got := r.DailyCap(t0, c.at); got != c.want {
			t.Errorf("cap at %v = %d, want %d", c.at, got, c.want)
		}
	}
	if got := r.DailyCap(time.Time{}, t0); got != 50 {
		t.Errorf("never connected: cap = %d", got)
	}
	r.Enabled = false
	if got := r.DailyCap(t0, t0); got != -1 {
		t.Errorf("disabled ramp cap = %d", got)
	}
}

func TestCallsAndPresencePatch(t *testing.T) {
	msg := "Não atendemos ligações; mande uma mensagem."
	p := Patch{}.Merge(Patch{Calls: &CallsPatch{AutoReject: ptr(true)}})
	p = p.Merge(Patch{Calls: &CallsPatch{RejectMessage: &msg}, Presence: &PresencePatch{Online: ptr(true)}})
	s := Defaults().Apply(p)
	if !s.Calls.AutoReject || s.Calls.RejectMessage != msg || !s.Presence.Online {
		t.Fatalf("settings = %+v", s)
	}
	if d := Defaults(); d.Calls.AutoReject || d.Presence.Online {
		t.Fatal("calls and presence must be off by default")
	}
	s.Calls.RejectMessage = strings.Repeat("x", MaxRejectMessage+1)
	if s.Validate() == nil {
		t.Fatal("long reject_message accepted")
	}
}

func TestAutoReadPatch(t *testing.T) {
	p := Patch{AutoRead: &AutoReadPatch{Enabled: ptr(true)}}
	p = p.Merge(Patch{AutoRead: &AutoReadPatch{Groups: ptr(true)}})
	s := Defaults().Apply(p)
	if !s.AutoRead.Enabled || !s.AutoRead.Groups {
		t.Fatalf("auto_read = %+v", s.AutoRead)
	}
	if d := Defaults(); d.AutoRead.Enabled || d.AutoRead.Groups {
		t.Fatal("auto_read must be off by default")
	}
	s = s.Apply(Patch{AutoRead: &AutoReadPatch{Enabled: ptr(false)}})
	if s.AutoRead.Enabled || !s.AutoRead.Groups {
		t.Fatalf("partial patch = %+v", s.AutoRead)
	}
}
