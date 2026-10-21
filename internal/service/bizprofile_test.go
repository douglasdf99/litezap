package service

import (
	"context"
	"strings"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
)

func sp(s string) *string      { return &s }
func fp(f float64) *float64    { return &f }
func lp(s ...string) *[]string { return &s }
func week(days map[string]BusinessDay) *BusinessWeek {
	return &BusinessWeek{Timezone: "America/Sao_Paulo", Days: days}
}

func TestBusinessSetRequest(t *testing.T) {
	req, fields, err := businessSetRequest(BusinessProfilePatch{
		Description: sp("Pizza"), Address: sp(" Rua A "), Email: sp("oi@loja.x"),
		Websites: lp("https://loja.x", " "), Latitude: fp(-23.5), Longitude: fp(-46.6), Categories: lp("10", "10", "20"),
		Hours: week(map[string]BusinessDay{
			"mon": {Mode: "open", Open: "09:00", Close: "18:00"},
			"tue": {Mode: "open", Intervals: []BusinessInterval{{"08:00", "12:00"}, {"14:00", "24:00"}}},
			"wed": {Mode: "24h"}, "thu": {Mode: "appointment"}, "sun": {Mode: "closed"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fields, ",") != "description,address,email,websites,location,categories,hours" {
		t.Fatalf("fields = %v", fields)
	}
	if *req.Address != "Rua A" || len(req.Websites) != 1 || !req.SetLocation || strings.Join(req.CategoryIds, ",") != "10,20" {
		t.Fatalf("req = %+v", req)
	}
	var got []string
	for _, d := range req.Hours.Days {
		got = append(got, d.Day+"/"+d.Mode+"/"+d.Open+"/"+d.Close)
	}
	want := "mon/specific_hours/540/1080 tue/specific_hours/480/720 tue/specific_hours/840/1440 wed/open_24h// thu/appointment_only//"
	if strings.Join(got, " ") != want {
		t.Fatalf("hours = %v", got)
	}

	// Partial: only what is sent.
	req, fields, err = businessSetRequest(BusinessProfilePatch{Websites: lp()})
	if err != nil || len(fields) != 1 || !req.SetWebsites || len(req.Websites) != 0 || req.Description != nil || req.Hours != nil {
		t.Fatalf("partial = %+v %v %v", req, fields, err)
	}

	for name, p := range map[string]BusinessProfilePatch{
		"empty":         {},
		"description":   {Description: sp(strings.Repeat("é", 513))},
		"email":         {Email: sp("Maria <m@x.y>")},
		"website":       {Websites: lp("javascript:alert(1)")},
		"3 websites":    {Websites: lp("https://a.x", "https://b.x", "https://c.x")},
		"lat only":      {Latitude: fp(1)},
		"lat range":     {Latitude: fp(100), Longitude: fp(0)},
		"no categories": {Categories: lp()},
		"category":      {Categories: lp("Varejo")},
		"timezone":      {Hours: &BusinessWeek{Days: map[string]BusinessDay{}}},
		"day key":       {Hours: week(map[string]BusinessDay{"monday": {Mode: "24h"}})},
		"mode":          {Hours: week(map[string]BusinessDay{"mon": {Mode: "specific_hours"}})},
		"time":          {Hours: week(map[string]BusinessDay{"mon": {Mode: "open", Open: "9h", Close: "18:00"}})},
		"same":          {Hours: week(map[string]BusinessDay{"mon": {Mode: "open", Open: "09:00", Close: "09:00"}})},
		"open 24:00":    {Hours: week(map[string]BusinessDay{"mon": {Mode: "open", Open: "24:00", Close: "09:00"}})},
		"both":          {Hours: week(map[string]BusinessDay{"mon": {Mode: "open", Open: "09:00", Close: "10:00", Intervals: []BusinessInterval{{"11:00", "12:00"}}}})},
	} {
		if _, _, err := businessSetRequest(p); apiCode(err) != "validation_failed" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBusinessWeekView(t *testing.T) {
	wk := businessWeek("America/Sao_Paulo", []*workerv1.BusinessHours{
		{Day: "mon", Mode: "specific_hours", Open: "540", Close: "1080"},
		{Day: "tue", Mode: "specific_hours", Open: "480", Close: "720"},
		{Day: "tue", Mode: "specific_hours", Open: "840", Close: "1440"},
		{Day: "wed", Mode: "open_24h"}, {Day: "thu", Mode: "appointment_only"},
	})
	if mon := wk.Days["mon"]; mon.Mode != "open" || mon.Open != "09:00" || mon.Close != "18:00" || wk.Days["sun"].Mode != "closed" ||
		wk.Days["wed"].Mode != "24h" || wk.Days["thu"].Mode != "appointment" {
		t.Fatalf("week = %+v", wk.Days)
	}
	if tue := wk.Days["tue"]; tue.Open != "" || len(tue.Intervals) != 2 || tue.Intervals[1] != (BusinessInterval{"14:00", "24:00"}) {
		t.Fatalf("split day = %+v", tue)
	}
	// The view round-trips into the same request.
	hs, err := businessHoursSet(*wk)
	if err != nil || len(hs.Days) != 5 || hs.Days[2].Close != "1440" {
		t.Fatalf("round trip = %+v, %v", hs, err)
	}
	if businessWeek("", nil) != nil {
		t.Fatal("no hours must be nil")
	}
}

func TestUpdateBusinessProfile(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Description: sp("x")}); apiCode(err) != "business_account_required" {
		t.Fatalf("personal account = %v", err)
	}
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{}); apiCode(err) != "validation_failed" {
		t.Fatalf("empty = %v", err)
	}
	e.wa().SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", Email: "old@loja.x", BusinessName: "Loja X"})
	v, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Description: sp("Pizza"), Categories: lp("7"),
		Latitude: fp(-23.5), Longitude: fp(-46.6), Hours: week(map[string]BusinessDay{"mon": {Mode: "open", Open: "09:00", Close: "18:00"}})})
	if err != nil || v.Description != "Pizza" || v.Email != "old@loja.x" || v.CategoryList[0].ID != "7" || v.Location == nil ||
		v.BusinessHours.Days["mon"].Open != "09:00" || v.Hours[0].Open != "540" {
		t.Fatalf("update = %+v, %v", v, err)
	}
	sets := e.wa().BusinessSets()
	if len(sets) != 1 || sets[0].Email != nil || sets[0].GetDescription() != "Pizza" {
		t.Fatalf("worker requests = %v", sets)
	}
	evs := e.pub.of(events.TypeBusinessProfileUpdated)
	if len(evs) != 1 || strings.Join(evs[0].(events.BusinessProfileUpdated).Fields, ",") != "description,location,categories,hours" {
		t.Fatalf("events = %v", evs)
	}
	// Rate limited: 5 per minute (the refused personal-account call counted too).
	for i := 0; i < 3; i++ {
		if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Address: sp("Rua B")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Address: sp("Rua C")}); apiCode(err) != "rate_limited" {
		t.Fatalf("sixth change = %v", err)
	}
}
