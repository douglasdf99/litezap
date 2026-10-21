package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
)

func TestBusinessClear(t *testing.T) {
	req, fields, err := businessSetRequest(BusinessProfilePatch{Clear: []string{"location", "Hours", "email", "websites", "description", "address"}})
	if err != nil {
		t.Fatal(err)
	}
	if !req.ClearLocation || !req.ClearHours || req.GetEmail() != "" || req.Email == nil || !req.SetWebsites || len(req.Websites) != 0 ||
		req.Description == nil || req.Address == nil || req.LocationClear != "" {
		t.Fatalf("req = %+v", req)
	}
	if strings.Join(fields, ",") != "description,address,email,websites,location,hours" {
		t.Fatalf("fields = %v", fields)
	}
	req, _, err = businessSetRequest(BusinessProfilePatch{Clear: []string{"location"}, LocationClear: "empty_content", Description: sp("x")})
	if err != nil || req.LocationClear != "empty_content" || req.GetDescription() != "x" {
		t.Fatalf("strategy = %+v, %v", req, err)
	}
	for name, p := range map[string]BusinessProfilePatch{
		"unknown field":         {Clear: []string{"categories"}},
		"set and clear":         {Clear: []string{"email"}, Email: sp("a@b.c")},
		"set and clear loc":     {Clear: []string{"location"}, Latitude: fp(1), Longitude: fp(1)},
		"set and clear hours":   {Clear: []string{"hours"}, Hours: week(nil)},
		"strategy alone":        {LocationClear: "empty_content", Description: sp("x")},
		"bad strategy":          {Clear: []string{"location"}, LocationClear: "x"},
		"address with strategy": {Clear: []string{"location"}, LocationClear: "empty_address", Address: sp("Rua")},
	} {
		if _, _, err := businessSetRequest(p); apiCode(err) != "validation_failed" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBusinessPatchNulls(t *testing.T) {
	var p BusinessProfilePatch
	if err := json.Unmarshal([]byte(`{"clear":["location"],"description":"oi"}`), &p); err != nil || p.Clear[0] != "location" || *p.Description != "oi" {
		t.Fatalf("decode = %+v, %v", p, err)
	}
	for _, body := range []string{`{"hours":null}`, `{"latitude":null,"longitude":null}`, `{"location":null}`, `{"email":null}`, `{"categories":null}`} {
		err := json.Unmarshal([]byte(body), &p)
		if apiCode(err) != "validation_failed" || !strings.Contains(err.Error(), "clear") {
			t.Errorf("%s: %v", body, err)
		}
	}
	if err := json.Unmarshal([]byte(`{"nope":1}`), &p); err == nil {
		t.Fatal("unknown fields must be refused")
	}
}

func TestUpdateBusinessProfileClear(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	e.wa().SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", BusinessName: "Loja X"})
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Clear: []string{"location", "hours"}}); err != nil {
		t.Fatal(err)
	}
	sets := e.wa().BusinessSets()
	if len(sets) != 1 || !sets[0].ClearLocation || !sets[0].ClearHours {
		t.Fatalf("worker requests = %v", sets)
	}
	ev := e.pub.of(events.TypeBusinessProfileUpdated)[0].(events.BusinessProfileUpdated)
	if strings.Join(ev.Cleared, ",") != "location,hours" {
		t.Fatalf("event = %+v", ev)
	}
}
