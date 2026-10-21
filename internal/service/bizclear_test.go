package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
)

func TestBusinessClear(t *testing.T) {
	req, fields, err := businessSetRequest(BusinessProfilePatch{Clear: []string{"Hours", "email", "websites", "description", "address"}})
	if err != nil {
		t.Fatal(err)
	}
	if !req.ClearHours || req.GetEmail() != "" || req.Email == nil || !req.SetWebsites || len(req.Websites) != 0 ||
		req.Description == nil || req.Address == nil {
		t.Fatalf("req = %+v", req)
	}
	if strings.Join(fields, ",") != "description,address,email,websites,hours" {
		t.Fatalf("fields = %v", fields)
	}
	// The map pin cannot be removed from a linked device (real device, 2026-10-21).
	for _, clear := range [][]string{{"location"}, {"hours", " Location "}} {
		_, _, err := businessSetRequest(BusinessProfilePatch{Clear: clear, Description: sp("x")})
		if ae, ok := err.(*apierr.Error); !ok || ae.Status != http.StatusUnprocessableEntity || ae.Code != "not_supported" ||
			!strings.Contains(ae.Message, "WhatsApp Business app") {
			t.Fatalf("clear %v = %v", clear, err)
		}
	}
	for name, p := range map[string]BusinessProfilePatch{
		"unknown field":       {Clear: []string{"categories"}},
		"set and clear":       {Clear: []string{"email"}, Email: sp("a@b.c")},
		"set and clear hours": {Clear: []string{"hours"}, Hours: week(nil)},
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
	for _, body := range []string{`{"latitude":null,"longitude":null}`, `{"location":null}`} {
		if err := json.Unmarshal([]byte(body), &p); apiCode(err) != "not_supported" {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, body := range []string{`{"hours":null}`, `{"email":null}`, `{"categories":null}`} {
		err := json.Unmarshal([]byte(body), &p)
		if apiCode(err) != "validation_failed" || !strings.Contains(err.Error(), "clear") {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, body := range []string{`{"nope":1}`, `{"location_clear":"empty_content"}`} {
		if err := json.Unmarshal([]byte(body), &p); err == nil {
			t.Fatalf("%s: unknown fields must be refused", body)
		}
	}
}

func TestUpdateBusinessProfileClear(t *testing.T) {
	e := newSocialEnv(t)
	ctx := context.Background()
	e.wa().SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", BusinessName: "Loja X"})
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Clear: []string{"location", "hours"}}); apiCode(err) != "not_supported" {
		t.Fatalf("location clear = %v", err)
	}
	if n := len(e.wa().BusinessSets()); n != 0 {
		t.Fatalf("location clear reached the worker: %d", n)
	}
	if _, err := e.s.UpdateBusinessProfile(ctx, "i1", BusinessProfilePatch{Clear: []string{"hours"}}); err != nil {
		t.Fatal(err)
	}
	sets := e.wa().BusinessSets()
	if len(sets) != 1 || !sets[0].ClearHours {
		t.Fatalf("worker requests = %v", sets)
	}
	ev := e.pub.of(events.TypeBusinessProfileUpdated)[0].(events.BusinessProfileUpdated)
	if strings.Join(ev.Cleared, ",") != "hours" {
		t.Fatalf("event = %+v", ev)
	}
}
