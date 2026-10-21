package worker

import (
	"context"
	"strings"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestBuildBusinessProfileSetGolden(t *testing.T) {
	n, err := buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{
		Description: proto.String("Pizza artesanal"), Address: proto.String(" Rua A, 10 "), Email: proto.String("oi@loja.x"),
		SetWebsites: true, Websites: []string{"https://loja.x", "http://insta.x/loja"},
		SetLocation: true, Latitude: -23.5505, Longitude: -46.6333,
		SetCategories: true, CategoryIds: []string{"133436743388217", "133436743388217", "1"},
		Hours: &workerv1.BusinessHoursSet{Timezone: "America/Sao_Paulo", Days: []*workerv1.BusinessHours{
			{Day: "mon", Mode: "specific_hours", Open: "540", Close: "1080"},
			{Day: "tue", Mode: "open_24h"},
			{Day: "wed", Mode: "appointment_only"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `<business_profile mutation_type="delta" v="3">` +
		`<address>Rua A, 10</address><email>oi@loja.x</email><description>Pizza artesanal</description>` +
		`<website>https://loja.x</website><website>http://insta.x/loja</website>` +
		`<latitude>-23.5505</latitude><longitude>-46.6333</longitude>` +
		`<categories><category id="133436743388217"/><category id="1"/></categories>` +
		`<business_hours timezone="America/Sao_Paulo">` +
		`<business_hours_config close_time="1080" day_of_week="mon" mode="specific_hours" open_time="540"/>` +
		`<business_hours_config day_of_week="tue" mode="open_24h"/>` +
		`<business_hours_config day_of_week="wed" mode="appointment_only"/>` +
		`</business_hours></business_profile>`
	if got := n.String(); got != want {
		t.Fatalf("node =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildBusinessProfileSetPartial(t *testing.T) {
	// Only the fields that are set are sent (a delta): WhatsApp keeps the rest.
	n, err := buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{Email: proto.String("")})
	if err != nil || n.String() != `<business_profile mutation_type="delta" v="3"><email></email></business_profile>` {
		t.Fatalf("email clear = %s, %v", n.String(), err)
	}
	n, err = buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{SetWebsites: true})
	if err != nil || n.String() != `<business_profile mutation_type="delta" v="3"><website></website></business_profile>` {
		t.Fatalf("websites clear = %s, %v", n.String(), err)
	}
	// An empty week: every day closed.
	n, err = buildBusinessProfileSet(&workerv1.SetBusinessProfileRequest{Hours: &workerv1.BusinessHoursSet{Timezone: "UTC"}})
	if err != nil || n.String() != `<business_profile mutation_type="delta" v="3"><business_hours timezone="UTC"/></business_profile>` {
		t.Fatalf("closed week = %s, %v", n.String(), err)
	}
}

func TestBuildBusinessProfileSetValidation(t *testing.T) {
	long := strings.Repeat("a", 513)
	day := func(d, m, o, c string) *workerv1.BusinessHoursSet {
		return &workerv1.BusinessHoursSet{Timezone: "America/Sao_Paulo", Days: []*workerv1.BusinessHours{{Day: d, Mode: m, Open: o, Close: c}}}
	}
	for name, req := range map[string]*workerv1.SetBusinessProfileRequest{
		"empty":         {},
		"description":   {Description: &long},
		"email":         {Email: proto.String("not-an-email")},
		"websites 3":    {SetWebsites: true, Websites: []string{"https://a.x", "https://b.x", "https://c.x"}},
		"website ftp":   {SetWebsites: true, Websites: []string{"ftp://a.x"}},
		"latitude":      {SetLocation: true, Latitude: 91},
		"no categories": {SetCategories: true},
		"category id":   {SetCategories: true, CategoryIds: []string{"abc"}},
		"4 categories":  {SetCategories: true, CategoryIds: []string{"1", "2", "3", "4"}},
		"no timezone":   {Hours: &workerv1.BusinessHoursSet{}},
		"day":           {Hours: day("monday", "open_24h", "", "")},
		"mode":          {Hours: day("mon", "closed", "", "")},
		"no times":      {Hours: day("mon", "specific_hours", "", "")},
		"bad time":      {Hours: day("mon", "specific_hours", "09:00", "1080")},
		"time range":    {Hours: day("mon", "specific_hours", "540", "1441")},
		"same times":    {Hours: day("mon", "specific_hours", "540", "540")},
	} {
		if _, err := buildBusinessProfileSet(req); err == nil {
			t.Errorf("%s: accepted", name)
		} else if c, code := codeOf(err); c != codes.InvalidArgument || code != "validation_failed" {
			t.Errorf("%s: %v %v", name, c, code)
		}
	}
}

func TestParseBusinessProfileExtras(t *testing.T) {
	p := parseBusinessProfile(&waBinary.Node{Tag: "business_profile", Content: []waBinary.Node{{Tag: "profile",
		Attrs: waBinary.Attrs{"jid": types.NewJID("5511988887777", types.DefaultUserServer)},
		Content: []waBinary.Node{
			{Tag: "description", Content: []byte("Pizza")},
			{Tag: "website", Content: []byte("https://a.x")}, {Tag: "website", Content: []byte("https://b.x")},
			{Tag: "latitude", Content: []byte("-23.5")}, {Tag: "longitude", Content: []byte("-46.6")},
			{Tag: "profile_options", Content: []waBinary.Node{{Tag: "commerce_experience", Content: []byte("catalog")}}},
		}}}})
	if p == nil || p.Jid != "5511988887777@s.whatsapp.net" || p.Description != "Pizza" || len(p.Websites) != 2 ||
		!p.HasLocation || p.Latitude != -23.5 || p.Longitude != -46.6 || p.Options["commerce_experience"] != "catalog" {
		t.Fatalf("parsed = %+v", p)
	}
	if parseBusinessProfile(&waBinary.Node{Tag: "business_profile"}) != nil {
		t.Fatal("no profile must parse as nil")
	}
}

func TestSetBusinessProfile(t *testing.T) {
	w := newMsgAdapter(t)
	ctx := context.Background()
	var sets []waBinary.Node
	var setErr error
	w.social.bizIQ = func(_ context.Context, typ string, n waBinary.Node) (*waBinary.Node, error) {
		if typ == "set" {
			sets = append(sets, n)
			return &waBinary.Node{Tag: "iq"}, setErr
		}
		return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "business_profile", Content: []waBinary.Node{{
			Tag: "profile", Attrs: waBinary.Attrs{"jid": n.GetChildByTag("profile").Attrs["jid"]},
			Content: []waBinary.Node{{Tag: "description", Content: []byte("nova")}}}}}}}, nil
	}
	req := &workerv1.SetBusinessProfileRequest{Description: proto.String("nova")}
	if c, code := codeOf(errOf(w.SetBusinessProfile(ctx, req))); c != codes.FailedPrecondition || code != "business_account_required" {
		t.Fatalf("personal account = %v %v", c, code)
	}
	w.social.business = func() bool { return true }
	bp, err := w.SetBusinessProfile(ctx, req)
	if err != nil || bp.Description != "nova" || len(sets) != 1 || sets[0].Attrs["mutation_type"] != "delta" {
		t.Fatalf("set = %+v, %v (sets %v)", bp, err, sets)
	}
	if _, err := w.SetBusinessProfile(ctx, &workerv1.SetBusinessProfileRequest{}); err == nil || len(sets) != 1 {
		t.Fatalf("an empty change must not reach WhatsApp: %v", err)
	}
	setErr = whatsmeow.ErrIQNotAcceptable
	err = errOf(w.SetBusinessProfile(ctx, req))
	if c, code := codeOf(err); c != codes.InvalidArgument || code != "validation_failed" || !strings.Contains(err.Error(), "406") {
		t.Fatalf("406 = %v", err)
	}
	setErr = whatsmeow.ErrIQRateOverLimit
	if c, code := codeOf(errOf(w.SetBusinessProfile(ctx, req))); c != codes.ResourceExhausted || code != "rate_limited" {
		t.Fatalf("429 = %v %v", c, code)
	}
}
