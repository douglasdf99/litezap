package service

import (
	"context"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---- business profile: editing (marco 12) ----

// BusinessCategoryView is a business category with its id (the id is what PATCH takes).
type BusinessCategoryView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BusinessLocation is the profile's map pin.
type BusinessLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// BusinessInterval is an opening interval ("09:00" to "18:00"; "24:00" closes at midnight).
type BusinessInterval struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

// BusinessDay is one weekday of the business hours.
// mode: open (open/close, or intervals for split hours) | closed | 24h | appointment.
type BusinessDay struct {
	Mode      string             `json:"mode"`
	Open      string             `json:"open,omitempty"`
	Close     string             `json:"close,omitempty"`
	Intervals []BusinessInterval `json:"intervals,omitempty"`
}

// BusinessWeek is the business hours by weekday (sun..sat), as PATCH takes them.
type BusinessWeek struct {
	Timezone string                 `json:"timezone"`
	Days     map[string]BusinessDay `json:"days"`
}

// BusinessProfilePatch changes the business profile; absent (or null) fields are kept.
// websites and categories replace the current lists; hours replace the whole week (days
// left out are closed); latitude and longitude go together.
type BusinessProfilePatch struct {
	Description *string       `json:"description,omitempty"`
	Address     *string       `json:"address,omitempty"`
	Email       *string       `json:"email,omitempty"`
	Websites    *[]string     `json:"websites,omitempty"`
	Latitude    *float64      `json:"latitude,omitempty"`
	Longitude   *float64      `json:"longitude,omitempty"`
	Categories  *[]string     `json:"categories,omitempty"` // category ids (see category_list in GET)
	Hours       *BusinessWeek `json:"hours,omitempty"`
}

var (
	bizWeekdays     = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	bizTimezoneRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)
	bizCategoryRe   = regexp.MustCompile(`^[0-9]{1,32}$`)
	bizProfileQuota = []window{{time.Minute, 5}, {24 * time.Hour, 50}}
)

const (
	maxBizDescription = 512
	maxBizAddress     = 256
	maxBizEmail       = 128
	maxBizWebsite     = 256
	maxBizIntervals   = 3
)

// hhmm turns "HH:MM" into minutes from midnight ("24:00" is 1440).
func hhmm(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || hh > 24 || (hh == 24 && mm != 0) {
		return 0, false
	}
	return hh*60 + mm, true
}

// minutesHHMM turns WhatsApp's minutes from midnight into "HH:MM" ("" when not a number).
func minutesHHMM(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1440 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", n/60, n%60)
}

func validWebsite(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		!strings.ContainsAny(raw, " \t\r\n<>\"")
}

// businessSetRequest validates a patch and builds the worker request; fields lists what
// changes.
func businessSetRequest(p BusinessProfilePatch) (*workerv1.SetBusinessProfileRequest, []string, error) {
	req := &workerv1.SetBusinessProfileRequest{}
	var fields []string
	if p.Description != nil {
		if utf8.RuneCountInString(*p.Description) > maxBizDescription {
			return nil, nil, apierr.Validation("description: up to 512 characters")
		}
		req.Description, fields = p.Description, append(fields, "description")
	}
	if p.Address != nil {
		a := strings.TrimSpace(*p.Address)
		if utf8.RuneCountInString(a) > maxBizAddress {
			return nil, nil, apierr.Validation("address: up to 256 characters")
		}
		req.Address, fields = &a, append(fields, "address")
	}
	if p.Email != nil {
		e := strings.TrimSpace(*p.Email)
		if e != "" {
			if a, err := mail.ParseAddress(e); err != nil || a.Address != e || len(e) > maxBizEmail {
				return nil, nil, apierr.Validation("email must be a plain address such as contato@loja.com (up to 128 characters)")
			}
		}
		req.Email, fields = &e, append(fields, "email")
	}
	if p.Websites != nil {
		ws := make([]string, 0, len(*p.Websites))
		for _, w := range *p.Websites {
			if w = strings.TrimSpace(w); w == "" {
				continue
			}
			if !validWebsite(w) || len(w) > maxBizWebsite {
				return nil, nil, apierr.Validation("websites must be http(s) URLs (up to 256 characters)")
			}
			ws = append(ws, w)
		}
		if len(ws) > 2 {
			return nil, nil, apierr.Validation("up to 2 websites")
		}
		req.SetWebsites, req.Websites, fields = true, ws, append(fields, "websites")
	}
	if (p.Latitude == nil) != (p.Longitude == nil) {
		return nil, nil, apierr.Validation("latitude and longitude go together")
	}
	if p.Latitude != nil {
		la, lo := *p.Latitude, *p.Longitude
		if math.IsNaN(la) || math.IsNaN(lo) || la < -90 || la > 90 || lo < -180 || lo > 180 {
			return nil, nil, apierr.Validation("latitude must be within -90..90 and longitude within -180..180")
		}
		req.SetLocation, req.Latitude, req.Longitude, fields = true, la, lo, append(fields, "location")
	}
	if p.Categories != nil {
		ids := []string{}
		for _, id := range *p.Categories {
			id = strings.TrimSpace(id)
			if !bizCategoryRe.MatchString(id) {
				return nil, nil, apierr.Validation("categories are category ids (digits), as in category_list of GET /business/profile")
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 || len(ids) > 3 {
			return nil, nil, apierr.Validation("1 to 3 categories")
		}
		req.SetCategories, req.CategoryIds, fields = true, ids, append(fields, "categories")
	}
	if p.Hours != nil {
		hs, err := businessHoursSet(*p.Hours)
		if err != nil {
			return nil, nil, err
		}
		req.Hours, fields = hs, append(fields, "hours")
	}
	if len(fields) == 0 {
		return nil, nil, apierr.Validation("nothing to change: send at least one of description, address, email, websites, latitude/longitude, categories, hours")
	}
	return req, fields, nil
}

func businessHoursSet(wk BusinessWeek) (*workerv1.BusinessHoursSet, error) {
	tz := strings.TrimSpace(wk.Timezone)
	if !bizTimezoneRe.MatchString(tz) || len(tz) > 64 {
		return nil, apierr.Validation("hours.timezone is required (IANA name such as America/Sao_Paulo)")
	}
	for d := range wk.Days {
		if !slices.Contains(bizWeekdays, d) {
			return nil, apierr.Validation("hours.days keys are sun, mon, tue, wed, thu, fri, sat")
		}
	}
	out := &workerv1.BusinessHoursSet{Timezone: tz}
	for _, d := range bizWeekdays { // a stable order
		day, ok := wk.Days[d]
		if !ok {
			continue // closed
		}
		bad := func(msg string) error { return apierr.Validation("hours.days." + d + ": " + msg) }
		switch day.Mode {
		case "closed":
		case "24h":
			out.Days = append(out.Days, &workerv1.BusinessHours{Day: d, Mode: "open_24h"})
		case "appointment":
			out.Days = append(out.Days, &workerv1.BusinessHours{Day: d, Mode: "appointment_only"})
		case "open":
			ivs := day.Intervals
			if len(ivs) == 0 {
				ivs = []BusinessInterval{{Open: day.Open, Close: day.Close}}
			} else if day.Open != "" || day.Close != "" {
				return nil, bad("send open/close or intervals, not both")
			}
			if len(ivs) > maxBizIntervals {
				return nil, bad("up to 3 intervals")
			}
			for _, iv := range ivs {
				o, ok1 := hhmm(iv.Open)
				c, ok2 := hhmm(iv.Close)
				if !ok1 || !ok2 || o == 1440 {
					return nil, bad(`open and close are times such as "09:00" and "18:00"`)
				}
				if o == c {
					return nil, bad("open and close must differ (use mode 24h for all day)")
				}
				out.Days = append(out.Days, &workerv1.BusinessHours{Day: d, Mode: "specific_hours",
					Open: strconv.Itoa(o), Close: strconv.Itoa(c)})
			}
		default:
			return nil, bad("mode must be open, closed, 24h or appointment")
		}
	}
	return out, nil
}

// businessWeek turns WhatsApp's hours into the PATCH shape (nil without hours).
func businessWeek(tz string, hours []*workerv1.BusinessHours) *BusinessWeek {
	if tz == "" && len(hours) == 0 {
		return nil
	}
	wk := &BusinessWeek{Timezone: tz, Days: map[string]BusinessDay{}}
	for _, d := range bizWeekdays {
		wk.Days[d] = BusinessDay{Mode: "closed"}
	}
	for _, h := range hours {
		if !slices.Contains(bizWeekdays, h.Day) {
			continue
		}
		day := wk.Days[h.Day]
		switch h.Mode {
		case "open_24h":
			day = BusinessDay{Mode: "24h"}
		case "appointment_only":
			day = BusinessDay{Mode: "appointment"}
		case "specific_hours":
			iv := BusinessInterval{Open: minutesHHMM(h.Open), Close: minutesHHMM(h.Close)}
			if day.Mode != "open" {
				day = BusinessDay{Mode: "open", Open: iv.Open, Close: iv.Close}
			} else {
				if len(day.Intervals) == 0 {
					day.Intervals = []BusinessInterval{{Open: day.Open, Close: day.Close}}
					day.Open, day.Close = "", ""
				}
				day.Intervals = append(day.Intervals, iv)
			}
		default:
			continue
		}
		wk.Days[h.Day] = day
	}
	return wk
}

// businessView converts the worker's profile.
func businessView(p *workerv1.BusinessProfileInfo) BusinessAccountView {
	v := BusinessAccountView{JID: p.Jid, BusinessName: p.BusinessName, Address: p.Address, Email: p.Email,
		Categories: nonNil(p.Categories), Timezone: p.Timezone, Options: p.Options, Hours: []BusinessHoursView{},
		Description: p.Description, Websites: nonNil(p.Websites), CategoryList: []BusinessCategoryView{},
		BusinessHours: businessWeek(p.Timezone, p.Hours)}
	for _, h := range p.Hours {
		v.Hours = append(v.Hours, BusinessHoursView{Day: h.Day, Mode: h.Mode, Open: h.Open, Close: h.Close})
	}
	for _, c := range p.CategoryList {
		v.CategoryList = append(v.CategoryList, BusinessCategoryView{ID: c.Id, Name: c.Name})
	}
	if p.HasLocation {
		v.Location = &BusinessLocation{Latitude: p.Latitude, Longitude: p.Longitude}
	}
	return v
}

// UpdateBusinessProfile changes the account's own business profile (only the fields in
// the patch) and returns it read back from WhatsApp. Rate limited per instance.
func (s *Social) UpdateBusinessProfile(ctx context.Context, instanceID string, p BusinessProfilePatch) (BusinessAccountView, error) {
	req, fields, err := businessSetRequest(p)
	if err != nil {
		return BusinessAccountView{}, err
	}
	cli, err := s.client(ctx, instanceID)
	if err != nil {
		return BusinessAccountView{}, err
	}
	if !s.take("business_profile", instanceID, bizProfileQuota) {
		return BusinessAccountView{}, socialLimited("business profile change (5 per minute)")
	}
	c, cancel := socialCtx(ctx)
	defer cancel()
	out, err := cli.SetBusinessProfile(c, req)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
			if code, detail, _ := strings.Cut(st.Message(), ": "); code == "validation_failed" {
				return BusinessAccountView{}, apierr.Validation(redact(detail))
			}
		}
		return BusinessAccountView{}, socialErr(err)
	}
	v := businessView(out)
	if s.Events != nil {
		s.Events.Publish(instanceID, events.TypeBusinessProfileUpdated, events.BusinessProfileUpdated{
			Fields: fields, Profile: v, Source: "api", Timestamp: s.now().UTC()})
	}
	return v, nil
}
