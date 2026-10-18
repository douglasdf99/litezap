package settings

import (
	"strings"
	"testing"
)

func TestGroupLimits(t *testing.T) {
	s, err := Effective(Defaults(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Groups != (Groups{10, 50, 3, 10}) {
		t.Fatalf("defaults = %+v", s.Groups)
	}
	s, err = Effective(Defaults(), "", `{"groups":{"max_adds_per_hour":20}}`)
	if err != nil || s.Groups.MaxAddsPerHour != 20 || s.Groups.MaxAddsPerDay != 50 {
		t.Fatalf("override = %+v %v", s.Groups, err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Groups.MaxCreatesPerDay = 11
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "unsafe_pacing") {
		t.Fatalf("over ceiling = %v", err)
	}
	s.UnsafePacing = true
	if err := s.Validate(); err != nil {
		t.Fatalf("unsafe lifts the ceiling: %v", err)
	}
	s.Groups.MaxAddsPerHour = 100 // above the day
	if err := s.Validate(); err == nil {
		t.Fatal("hour above day accepted")
	}
	p := Patch{Groups: &GroupsPatch{MaxAddsPerDay: ptrInt(30)}}.Merge(Patch{Groups: &GroupsPatch{MaxJoinsPerDay: ptrInt(5)}})
	if *p.Groups.MaxAddsPerDay != 30 || *p.Groups.MaxJoinsPerDay != 5 || p.Groups.MaxAddsPerHour != nil {
		t.Fatalf("merge = %+v", p.Groups)
	}
}

func ptrInt(v int) *int { return &v }
