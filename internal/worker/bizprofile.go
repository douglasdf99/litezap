package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc/codes"
)

// The business profile (marco 12). whatsmeow reads it (GetBusinessProfile) but has no
// setter, and its parser drops the description, the websites and the location, so both
// directions are built here on the same IQ namespace:
//
//	read:  <iq xmlns="w:biz" type="get" to="s.whatsapp.net">
//	         <business_profile v="244"><profile jid="…"/></business_profile></iq>
//	write: <iq xmlns="w:biz" type="set" to="s.whatsapp.net">
//	         <business_profile v="3" mutation_type="delta">
//	           <address>…</address><email>…</email><description>…</description>
//	           <website>…</website>…<latitude>…</latitude><longitude>…</longitude>
//	           <categories><category id="…"/></categories>
//	           <business_hours timezone="America/Sao_Paulo">
//	             <business_hours_config day_of_week="mon" mode="specific_hours" open_time="540" close_time="1080"/>
//	           </business_hours>
//	         </business_profile></iq>
//
// The write mirrors Baileys' updateBussinesProfile and WA Web's WAWebBusinessProfileJob
// (v="3", mutation_type="delta", times in minutes from midnight). A real-device test on
// 2026-10-21 confirmed description, address, latitude/longitude and hours; "" clears the
// description and the address, and a week without days leaves the timezone with no hours.
//
// Clears (device validation pending). WA Web never removes the map pin (its form says
// "Edit map on mobile") and always sends a timezone with the hours, so neither clear has
// a reference encoding. WA Web does clear the websites with a contentless <website/>,
// which is why that form is tried first here:
//   - location, by strategy (LocationClear*): <latitude/><longitude/> (no content), then
//     <latitude></latitude><longitude></longitude> (empty content), then an empty
//     <address/> without the coordinates. The supervisor picks one (config or request).
//   - hours: <business_hours/> with no timezone and no configs.
const (
	bizNamespace      = "w:biz"
	bizReadVersion    = "244" // as whatsmeow and Baileys read it
	bizWriteVersion   = "3"   // as Baileys writes it
	maxBizDescription = 512
	maxBizAddress     = 256
	maxBizEmail       = 128
	maxBizWebsite     = 256
	maxBizWebsites    = 2
	maxBizCategories  = 3
	maxBizHourEntries = 21 // 7 days, up to 3 intervals each
)

// Location clear strategies (SetBusinessProfileRequest.location_clear).
const (
	LocationClearEmptyElements = "empty_elements" // <latitude/><longitude/>: the default
	LocationClearEmptyContent  = "empty_content"  // <latitude></latitude><longitude></longitude>
	LocationClearEmptyAddress  = "empty_address"  // <address/> without the coordinates
)

// LocationClearStrategies lists the strategies in the order to try them on a device.
var LocationClearStrategies = []string{LocationClearEmptyElements, LocationClearEmptyContent, LocationClearEmptyAddress}

// locationClearNodes encodes "remove the map pin" with a strategy.
func locationClearNodes(strategy string) ([]waBinary.Node, error) {
	switch strategy {
	case "", LocationClearEmptyElements:
		return []waBinary.Node{{Tag: "latitude"}, {Tag: "longitude"}}, nil
	case LocationClearEmptyContent:
		return []waBinary.Node{{Tag: "latitude", Content: []byte{}}, {Tag: "longitude", Content: []byte{}}}, nil
	case LocationClearEmptyAddress:
		return []waBinary.Node{{Tag: "address"}}, nil
	}
	return nil, invalid("location_clear must be empty_elements, empty_content or empty_address")
}

var bizDays = map[string]bool{"sun": true, "mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true}

// bizIQ sends a w:biz IQ to the server and returns the answer.
func (w *Whatsmeow) bizIQ(ctx context.Context, typ string, content waBinary.Node) (*waBinary.Node, error) {
	if h := w.social.bizIQ; h != nil {
		return h(ctx, typ, content)
	}
	return w.cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: bizNamespace, Type: whatsmeow.DangerousInfoQueryType(typ), To: types.ServerJID,
		Content: []waBinary.Node{content},
	})
}

// fetchBusinessProfile reads a business profile with the same IQ as whatsmeow.
func (w *Whatsmeow) fetchBusinessProfile(ctx context.Context, jid types.JID) (*workerv1.BusinessProfileInfo, error) {
	resp, err := w.bizIQ(ctx, "get", waBinary.Node{
		Tag: "business_profile", Attrs: waBinary.Attrs{"v": bizReadVersion},
		Content: []waBinary.Node{{Tag: "profile", Attrs: waBinary.Attrs{"jid": jid}}},
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	node, ok := resp.GetOptionalChildByTag("business_profile")
	if !ok {
		return nil, nil
	}
	return parseBusinessProfile(&node), nil
}

// nodeText is the text content of a node.
func nodeText(n waBinary.Node) string {
	switch c := n.Content.(type) {
	case []byte:
		return string(c)
	case string:
		return c
	}
	return ""
}

// parseBusinessProfile parses <business_profile><profile …/></business_profile> like
// whatsmeow's parser, plus description, websites and location. nil when there is no
// profile (not a business).
func parseBusinessProfile(node *waBinary.Node) *workerv1.BusinessProfileInfo {
	p, ok := node.GetOptionalChildByTag("profile")
	if !ok {
		return nil
	}
	out := &workerv1.BusinessProfileInfo{}
	if jid, ok := p.AttrGetter().GetJID("jid", false); ok {
		out.Jid = jid.ToNonAD().String()
	}
	var lat, lng string
	for _, c := range p.GetChildren() {
		switch c.Tag {
		case "address":
			out.Address = nodeText(c)
		case "email":
			out.Email = nodeText(c)
		case "description":
			out.Description = nodeText(c)
		case "website":
			if s := strings.TrimSpace(nodeText(c)); s != "" {
				out.Websites = append(out.Websites, s)
			}
		case "latitude":
			lat = strings.TrimSpace(nodeText(c))
		case "longitude":
			lng = strings.TrimSpace(nodeText(c))
		case "categories":
			for _, cat := range c.GetChildren() {
				if cat.Tag != "category" {
					continue
				}
				id, name := cat.AttrGetter().OptionalString("id"), nodeText(cat)
				out.Categories = append(out.Categories, name)
				out.CategoryList = append(out.CategoryList, &workerv1.BusinessCategory{Id: id, Name: name})
			}
		case "business_hours":
			out.Timezone = c.AttrGetter().OptionalString("timezone")
			for _, cfg := range c.GetChildren() {
				if cfg.Tag != "business_hours_config" {
					continue
				}
				ag := cfg.AttrGetter()
				out.Hours = append(out.Hours, &workerv1.BusinessHours{Day: ag.OptionalString("day_of_week"),
					Mode: ag.OptionalString("mode"), Open: ag.OptionalString("open_time"), Close: ag.OptionalString("close_time")})
			}
		case "profile_options":
			for _, o := range c.GetChildren() {
				if out.Options == nil {
					out.Options = map[string]string{}
				}
				out.Options[o.Tag] = nodeText(o)
			}
		}
	}
	if la, err1 := strconv.ParseFloat(lat, 64); err1 == nil {
		if lo, err2 := strconv.ParseFloat(lng, 64); err2 == nil && validLatLng(la, lo) {
			out.HasLocation, out.Latitude, out.Longitude = true, la, lo
		}
	}
	return out
}

func validLatLng(lat, lng float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lng) && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// minutes parses a time of day in minutes from midnight (0-1440).
func minutes(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n <= 1440 && strconv.Itoa(n) == s
}

// buildBusinessProfileSet builds the delta mutation for req (validated: the worker never
// sends a value WhatsApp's apps could not).
func buildBusinessProfileSet(req *workerv1.SetBusinessProfileRequest) (waBinary.Node, error) {
	var kids []waBinary.Node
	switch {
	case req.ClearLocation && req.SetLocation:
		return waBinary.Node{}, invalid("location: set or clear, not both")
	case req.ClearHours && req.Hours != nil:
		return waBinary.Node{}, invalid("hours: set or clear, not both")
	case req.ClearLocation && req.Address != nil && req.LocationClear == LocationClearEmptyAddress:
		return waBinary.Node{}, invalid("the empty_address location clear also clears the address: do not send an address with it")
	case !req.ClearLocation && req.LocationClear != "":
		return waBinary.Node{}, invalid("location_clear only applies when clearing the location")
	}
	text := func(tag, v string, max int) error {
		if utf8.RuneCountInString(v) > max || strings.ContainsRune(v, 0) {
			return invalid(fmt.Sprintf("%s: up to %d characters", tag, max))
		}
		kids = append(kids, waBinary.Node{Tag: tag, Content: []byte(v)})
		return nil
	}
	if req.Address != nil {
		if err := text("address", strings.TrimSpace(*req.Address), maxBizAddress); err != nil {
			return waBinary.Node{}, err
		}
	}
	if req.Email != nil {
		e := strings.TrimSpace(*req.Email)
		if e != "" && (!strings.Contains(e, "@") || strings.ContainsAny(e, " \t\r\n<>")) {
			return waBinary.Node{}, invalid("email is not valid")
		}
		if err := text("email", e, maxBizEmail); err != nil {
			return waBinary.Node{}, err
		}
	}
	if req.Description != nil {
		if err := text("description", *req.Description, maxBizDescription); err != nil {
			return waBinary.Node{}, err
		}
	}
	if req.SetWebsites {
		if len(req.Websites) > maxBizWebsites {
			return waBinary.Node{}, invalid("up to 2 websites")
		}
		for _, u := range req.Websites {
			u = strings.TrimSpace(u)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") || strings.ContainsAny(u, " \t\r\n<>\"") {
				return waBinary.Node{}, invalid("websites must be http(s) URLs")
			}
			if err := text("website", u, maxBizWebsite); err != nil {
				return waBinary.Node{}, err
			}
		}
		if len(req.Websites) == 0 {
			// Clearing: an empty website (the apps' "remove website").
			kids = append(kids, waBinary.Node{Tag: "website", Content: []byte{}})
		}
	}
	if req.SetLocation {
		if !validLatLng(req.Latitude, req.Longitude) {
			return waBinary.Node{}, invalid("latitude must be within -90..90 and longitude within -180..180")
		}
		kids = append(kids,
			waBinary.Node{Tag: "latitude", Content: []byte(strconv.FormatFloat(req.Latitude, 'f', -1, 64))},
			waBinary.Node{Tag: "longitude", Content: []byte(strconv.FormatFloat(req.Longitude, 'f', -1, 64))})
	}
	if req.ClearLocation {
		nodes, err := locationClearNodes(req.LocationClear)
		if err != nil {
			return waBinary.Node{}, err
		}
		kids = append(kids, nodes...)
	}
	if req.SetCategories {
		if len(req.CategoryIds) == 0 || len(req.CategoryIds) > maxBizCategories {
			return waBinary.Node{}, invalid("1 to 3 categories")
		}
		cats := make([]waBinary.Node, 0, len(req.CategoryIds))
		seen := map[string]bool{}
		for _, id := range req.CategoryIds {
			if id == "" || len(id) > 32 || strings.Trim(id, "0123456789") != "" {
				return waBinary.Node{}, invalid("category ids are numeric")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			cats = append(cats, waBinary.Node{Tag: "category", Attrs: waBinary.Attrs{"id": id}})
		}
		kids = append(kids, waBinary.Node{Tag: "categories", Content: cats})
	}
	if hs := req.Hours; hs != nil {
		tz := strings.TrimSpace(hs.Timezone)
		if tz == "" || len(tz) > 64 || strings.ContainsAny(tz, " \t\r\n<>\"") {
			return waBinary.Node{}, invalid("hours need a timezone (e.g. America/Sao_Paulo)")
		}
		if len(hs.Days) > maxBizHourEntries {
			return waBinary.Node{}, invalid("too many hours entries")
		}
		cfgs := make([]waBinary.Node, 0, len(hs.Days))
		for _, d := range hs.Days {
			if !bizDays[d.Day] {
				return waBinary.Node{}, invalid("day must be sun, mon, tue, wed, thu, fri or sat")
			}
			attrs := waBinary.Attrs{"day_of_week": d.Day, "mode": d.Mode}
			switch d.Mode {
			case "specific_hours":
				o, ok1 := minutes(d.Open)
				c, ok2 := minutes(d.Close)
				if !ok1 || !ok2 || o == c {
					return waBinary.Node{}, invalid("specific_hours need open and close times (minutes from midnight, different)")
				}
				attrs["open_time"], attrs["close_time"] = d.Open, d.Close
			case "open_24h", "appointment_only":
			default:
				return waBinary.Node{}, invalid("mode must be specific_hours, open_24h or appointment_only")
			}
			cfgs = append(cfgs, waBinary.Node{Tag: "business_hours_config", Attrs: attrs})
		}
		kids = append(kids, waBinary.Node{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": tz}, Content: cfgs})
	}
	if req.ClearHours {
		kids = append(kids, waBinary.Node{Tag: "business_hours"})
	}
	if len(kids) == 0 {
		return waBinary.Node{}, invalid("nothing to change")
	}
	return waBinary.Node{
		Tag:     "business_profile",
		Attrs:   waBinary.Attrs{"v": bizWriteVersion, "mutation_type": "delta"},
		Content: kids,
	}, nil
}

// bizSetErr maps a refusal of the profile mutation.
func bizSetErr(err error) error {
	var iq *whatsmeow.IQError
	if errors.As(err, &iq) && (iq.Code == 400 || iq.Code == 406) {
		return grpcErr(codes.InvalidArgument, "validation_failed",
			fmt.Sprintf("WhatsApp rejected the business profile (%d %s)", iq.Code, iq.Text))
	}
	return socialErr(err)
}

// SetBusinessProfile changes the account's own business profile and reads it back.
func (w *Whatsmeow) SetBusinessProfile(ctx context.Context, req *workerv1.SetBusinessProfileRequest) (*workerv1.BusinessProfileInfo, error) {
	pn, _ := w.ownJIDs()
	if pn.IsEmpty() {
		return nil, grpcErr(codes.FailedPrecondition, "logged_out", "session is not logged in")
	}
	if !w.socialAPI().business() {
		return nil, grpcErr(codes.FailedPrecondition, "business_account_required", "not a WhatsApp Business account")
	}
	node, err := buildBusinessProfileSet(req)
	if err != nil {
		return nil, err
	}
	if _, err := w.bizIQ(ctx, "set", node); err != nil {
		return nil, bizSetErr(err)
	}
	return w.BusinessProfile(ctx, "")
}
