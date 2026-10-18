package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/workertest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const contact = "5511988887777@s.whatsapp.net"

func chatData(t *testing.T, ev sseEvent) events.ChatUpdated {
	t.Helper()
	var env struct{ Data events.ChatUpdated }
	if err := json.Unmarshal([]byte(ev.data), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

// inbound logs a received message in chat and waits until it is in the log.
func (e *env) inbound(id, waID, chat string) {
	e.t.Helper()
	e.sp.Last(id).EmitMessage(&workerv1.Message{Id: waID, Chat: chat, Sender: chat, Type: "text", Text: "oi",
		TimestampMs: time.Now().UnixMilli(), IsGroup: strings.HasSuffix(chat, "@g.us")})
	e.eventually("message logged", func() bool {
		_, err := e.st.MessageByWAID(context.Background(), id, chat, waID)
		return err == nil
	})
}

func TestChatActions(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Chats")
	f := e.sp.Last(id)
	f.WA.SetChatEcho(true) // like whatsmeow: the app state echo of our own action comes back
	e.inbound(id, "W1", contact)
	sse, _ := e.sse("/v1/instances/"+id+"/events?types=chat.updated", tok)
	base := "/v1/instances/" + id + "/chats/" + contact

	code, v := e.do("PATCH", base, tok, `{"archived":true}`)
	if code != 200 || v["archived"] != true || v["pinned"] != false {
		t.Fatalf("archive = %d %v", code, v)
	}
	acts := f.WA.ChatActions()
	if len(acts) != 1 || acts[0].Action != "archive" || acts[0].Chat != contact || acts[0].Message.GetId() != "W1" {
		t.Fatalf("actions = %+v", acts)
	}
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); !ev.Archived || ev.Source != "api" || ev.Changed[0] != "archived" || ev.Chat != contact {
		t.Fatalf("chat.updated = %+v", ev)
	}
	// Mute for an hour, then a phone change (pin) arrives through the app state.
	code, v = e.do("PATCH", base, tok, `{"muted":true,"mute_seconds":3600}`)
	if code != 200 || v["muted"] != true || v["muted_until"] == nil {
		t.Fatalf("mute = %d %v", code, v)
	}
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); !ev.Muted || ev.MutedUntil == nil || ev.Changed[0] != "muted" {
		t.Fatalf("mute event = %+v", ev)
	}
	f.EmitChatUpdate(&workerv1.ChatUpdate{Chat: contact, Field: "archived", Value: false})
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); ev.Archived || ev.Source != "device" {
		t.Fatalf("phone unarchive = %+v", ev)
	}
	f.EmitChatUpdate(&workerv1.ChatUpdate{Chat: contact, Field: "pinned", Value: true})
	if ev := chatData(t, sse.next(events.TypeChatUpdated)); !ev.Pinned || ev.Source != "device" {
		t.Fatalf("phone pin = %+v", ev)
	}
	code, v = e.do("GET", base, tok, "")
	if code != 200 || v["pinned"] != true || v["archived"] != false || v["muted"] != true {
		t.Fatalf("get chat = %d %v", code, v)
	}
	_, list := e.do("GET", "/v1/instances/"+id+"/chats", tok, "")
	chats := list["chats"].([]any)
	if c := chats[0].(map[string]any); c["pinned"] != true || c["muted"] != true {
		t.Fatalf("list = %v", chats)
	}
	// Unread, then read again (read receipts go out too).
	if code, v = e.do("PATCH", base, tok, `{"unread":true}`); code != 200 || v["unread"] != true {
		t.Fatalf("unread = %d %v", code, v)
	}
	if code, v = e.do("PATCH", base, tok, `{"unread":false}`); code != 200 || v["unread"] != false || v["unread_count"] != float64(0) {
		t.Fatalf("read = %d %v", code, v)
	}
	if reads := f.WA.Reads(); len(reads) == 0 || reads[len(reads)-1].MessageIds[0] != "W1" {
		t.Fatalf("read receipts = %+v", reads)
	}
	// Validation.
	for body, want := range map[string]string{
		`{}`:                                 "validation_failed",
		`{"archive":true}`:                   "validation_failed", // typo: unknown field
		`{"mute_seconds":60}`:                "validation_failed",
		`{"archived":true,"pinned":true}`:    "validation_failed",
		`{"muted":true,"mute_seconds":-1}`:   "validation_failed",
		`{"muted":true,"mute_seconds":"1h"}`: "validation_failed",
	} {
		if code, v := e.do("PATCH", base, tok, body); code != 400 || errCode(v) != want {
			t.Errorf("%s = %d %v", body, code, v)
		}
	}
	if code, v := e.do("PATCH", "/v1/instances/"+id+"/chats/5511000000000@s.whatsapp.net", tok, `{"pinned":true}`); code != 404 {
		t.Fatalf("unknown chat = %d %v", code, v)
	}
	// Delete: the chat leaves the list (the log keeps the messages).
	if code, v := e.do("DELETE", base+"?delete_media=1", tok, ""); code != 204 {
		t.Fatalf("delete = %d %v", code, v)
	}
	if a := f.WA.ChatActions(); a[len(a)-1].Action != "delete" || !a[len(a)-1].DeleteMedia {
		t.Fatalf("delete action = %+v", a[len(a)-1])
	}
	_, list = e.do("GET", "/v1/instances/"+id+"/chats", tok, "")
	if len(list["chats"].([]any)) != 0 {
		t.Fatalf("deleted chat listed: %v", list)
	}
	if code, _ := e.do("GET", base+"/messages", tok, ""); code != 200 {
		t.Fatal("messages of a deleted chat stay readable")
	}
}

func TestChatPinLimitAndErrors(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Pins")
	f := e.sp.Last(id)
	chats := []string{"5511900000001@s.whatsapp.net", "5511900000002@s.whatsapp.net", "5511900000003@s.whatsapp.net", "5511900000004@s.whatsapp.net"}
	for i, c := range chats {
		e.inbound(id, "P"+string(rune('0'+i)), c)
	}
	for _, c := range chats[:3] {
		if code, v := e.do("PATCH", "/v1/instances/"+id+"/chats/"+c, tok, `{"pinned":true}`); code != 200 {
			t.Fatalf("pin = %d %v", code, v)
		}
	}
	if code, v := e.do("PATCH", "/v1/instances/"+id+"/chats/"+chats[3], tok, `{"pinned":true}`); code != 409 || errCode(v) != "pin_limit" {
		t.Fatalf("fourth pin = %d %v", code, v)
	}
	f.WA.SetAccountErr(status.Error(codes.FailedPrecondition, "app_state_not_synced: keys not synced"))
	if code, v := e.do("PATCH", "/v1/instances/"+id+"/chats/"+chats[3], tok, `{"archived":true}`); code != 409 || errCode(v) != "app_state_not_synced" {
		t.Fatalf("not synced = %d %v", code, v)
	}
	// Nothing changed in the log when WhatsApp refused.
	if _, v := e.do("GET", "/v1/instances/"+id+"/chats/"+chats[3], tok, ""); v["archived"] != false {
		t.Fatalf("chat = %v", v)
	}
	// Another instance's token cannot touch these chats.
	_, other := e.create("Outra")
	if code, _ := e.do("PATCH", "/v1/instances/"+id+"/chats/"+chats[0], other, `{"pinned":false}`); code != 403 {
		t.Fatalf("other token = %d", code)
	}
}

// GET /chats/{jid} adopts the worker's local chat settings (they include the initial
// full sync, which produces no events).
func TestGetChatReconcilesWorkerState(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Sync")
	e.inbound(id, "S1", contact)
	e.sp.Last(id).WA.SetChatSettings(contact, &workerv1.GetChatSettingsResponse{Found: true, Archived: true, MutedUntilMs: -1})
	code, v := e.do("GET", "/v1/instances/"+id+"/chats/"+contact, tok, "")
	if code != 200 || v["archived"] != true || v["muted"] != true || v["muted_until"] != nil {
		t.Fatalf("chat = %d %v", code, v)
	}
}

func TestStarMessage(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Star")
	f := e.sp.Last(id)
	e.inbound(id, "W1", contact)
	m, _ := e.st.MessageByWAID(context.Background(), id, contact, "W1")
	code, v := e.do("PUT", "/v1/instances/"+id+"/messages/"+m.ID+"/star", tok, `{"starred":true}`)
	if code != 200 || v["starred"] != true {
		t.Fatalf("star = %d %v", code, v)
	}
	if a := f.WA.ChatActions(); len(a) != 1 || a[0].Action != "star" || a[0].Message.GetId() != "W1" {
		t.Fatalf("actions = %+v", a)
	}
	// Unstarred on the phone.
	f.EmitChatUpdate(&workerv1.ChatUpdate{Chat: contact, Field: "starred", Value: false, MessageId: "W1"})
	e.eventually("unstarred", func() bool {
		_, v := e.do("GET", "/v1/instances/"+id+"/messages/"+m.ID, tok, "")
		return v["starred"] == nil
	})
	if code, _ := e.do("PUT", "/v1/instances/"+id+"/messages/"+m.ID+"/star", tok, `{}`); code != 400 {
		t.Fatalf("missing starred = %d", code)
	}
}

func TestContactPictureAndProfile(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Contatos")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id + "/contacts/"
	// No picture: 404 not_found (never 502), and the miss is cached.
	if code, v := e.do("GET", base+contact+"/picture", tok, ""); code != 404 || errCode(v) != "not_found" {
		t.Fatalf("no picture = %d %v", code, v)
	}
	if code, _ := e.do("GET", base+contact+"/picture", tok, ""); code != 404 || f.WA.Lookups() != 1 {
		t.Fatalf("miss not cached: lookups %d", f.WA.Lookups())
	}
	other := "5511977776666@s.whatsapp.net"
	f.WA.SetPicture(other, workertest.FakePicture{ID: "P1", URL: "https://pps.whatsapp.net/p1.jpg", Data: []byte("JPEGBYTES")})
	code, v := e.do("GET", base+"5511977776666/picture", tok, "") // phone numbers work too
	if code != 200 || v["id"] != "P1" || v["url"] != "https://pps.whatsapp.net/p1.jpg" || v["jid"] != other {
		t.Fatalf("picture = %d %v", code, v)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+base+other+"/picture?download=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "JPEGBYTES" || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("download = %d %q %s", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	n := f.WA.Lookups()
	resp, _ = http.DefaultClient.Do(req) // cached file: no new lookup
	resp.Body.Close()
	if f.WA.Lookups() != n {
		t.Fatalf("cached download looked up again")
	}

	f.WA.SetUserInfo(other, &workerv1.GetUserInfoResponse{Jid: other, About: "Disponível", PictureId: "P1",
		VerifiedName: "Loja X", Business: &workerv1.BusinessProfile{Email: "a@b.c", Categories: []string{"Varejo"}}})
	code, v = e.do("GET", base+other+"/profile", tok, "")
	if code != 200 || v["about"] != "Disponível" || v["verified_name"] != "Loja X" || v["business"].(map[string]any)["email"] != "a@b.c" {
		t.Fatalf("profile = %d %v", code, v)
	}
	if code, v := e.do("GET", base+"120363@g.us/profile", tok, ""); code != 400 {
		t.Fatalf("group profile = %d %v", code, v)
	}
	// The lookup quota: 30 per minute per instance.
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		code, v := e.do("GET", base+other+"/profile", tok, "")
		limited = code == 429 && errCode(v) == "rate_limited"
	}
	if !limited {
		t.Fatal("lookup quota never applied")
	}
}

func TestBlocklist(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Bloqueio")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id
	code, v := e.do("POST", base+"/contacts/"+contact+"/block", tok, "")
	if code != 200 || len(v["blocked"].([]any)) != 1 || !f.WA.Blocked(contact) {
		t.Fatalf("block = %d %v", code, v)
	}
	if _, v := e.do("GET", base+"/blocklist", tok, ""); v["blocked"].([]any)[0] != contact {
		t.Fatalf("blocklist = %v", v)
	}
	if code, v := e.do("POST", base+"/contacts/"+contact+"/unblock", tok, ""); code != 200 || len(v["blocked"].([]any)) != 0 {
		t.Fatalf("unblock = %d %v", code, v)
	}
	if code, _ := e.do("POST", base+"/contacts/120363@g.us/block", tok, ""); code != 400 {
		t.Fatalf("block group = %d", code)
	}
}

func pngB64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, h/2, color.RGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestOwnProfile(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Perfil")
	f := e.sp.Last(id)
	f.WA.SetProfile(&workerv1.GetProfileResponse{Jid: "5511999999999@s.whatsapp.net", PushName: "Loja", Platform: "android"})
	base := "/v1/instances/" + id + "/profile"
	code, v := e.do("GET", base, tok, "")
	if code != 200 || v["name"] != "Loja" || v["platform"] != "android" || v["picture"] != nil {
		t.Fatalf("profile = %d %v", code, v)
	}
	code, v = e.do("PATCH", base, tok, `{"name":"Loja Nova","about":"Atendimento 9-18h"}`)
	if code != 200 || v["name"] != "Loja Nova" || v["about"] != "Atendimento 9-18h" {
		t.Fatalf("patch = %d %v", code, v)
	}
	if code, _ := e.do("PATCH", base, tok, `{"name":"`+strings.Repeat("x", 26)+`"}`); code != 400 {
		t.Fatalf("long name = %d", code)
	}
	if code, _ := e.do("PATCH", base, tok, `{"about":"`+strings.Repeat("x", 140)+`"}`); code != 400 {
		t.Fatalf("long about = %d", code)
	}
	code, v = e.do("PUT", base+"/picture", tok, `{"base64":"`+pngB64(t, 1200, 800)+`"}`)
	if code != 200 {
		t.Fatalf("picture = %d %v", code, v)
	}
	_, pic := f.WA.ProfileUpdates()
	img, err := jpeg.Decode(bytes.NewReader(pic))
	if err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 640 {
		t.Fatalf("uploaded picture: %v %v", err, img)
	}
	if code, _ := e.do("PUT", base+"/picture", tok, `{"base64":"bm90IGFuIGltYWdl"}`); code != 400 {
		t.Fatalf("not an image = %d", code)
	}
	// The own picture downloads through the API without spending the contact quota.
	if code, v := e.do("GET", base+"/picture", tok, ""); code != 404 || errCode(v) != "not_found" {
		t.Fatalf("own picture missing = %d %v", code, v)
	}
	f.WA.SetPicture("", workertest.FakePicture{ID: "ME", URL: "https://pps.whatsapp.net/me.jpg", Data: []byte("MYJPEG")})
	req, _ := http.NewRequest("GET", e.srv.URL+base+"/picture?preview=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "MYJPEG" {
		t.Fatalf("own picture = %d %q", resp.StatusCode, b)
	}
	if code, _ := e.do("DELETE", base+"/picture", tok, ""); code != 204 {
		t.Fatalf("remove = %d", code)
	}
	ups, _ := f.WA.ProfileUpdates()
	if !ups[len(ups)-1].RemovePicture {
		t.Fatalf("updates = %+v", ups)
	}
}

func TestPrivacySettings(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Privacidade")
	f := e.sp.Last(id)
	base := "/v1/instances/" + id + "/privacy"
	code, v := e.do("GET", base, tok, "")
	if code != 200 || v["last_seen"] != "contacts" || v["read_receipts"] != "all" || v["groups_add"] != "all" {
		t.Fatalf("privacy = %d %v", code, v)
	}
	code, v = e.do("PATCH", base, tok, `{"read_receipts":"none","last_seen":"none","default_disappearing_seconds":86400}`)
	if code != 200 || v["read_receipts"] != "none" || v["last_seen"] != "none" || f.WA.DefaultDisappearing() != 86400 {
		t.Fatalf("patch = %d %v", code, v)
	}
	for _, body := range []string{`{"read_receipts":"contacts"}`, `{"nope":"all"}`, `{"default_disappearing_seconds":5}`, `{}`} {
		if code, v := e.do("PATCH", base, tok, body); code != 400 {
			t.Errorf("%s = %d %v", body, code, v)
		}
	}
}

func TestAutoReadSetting(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Leitura")
	code, set := e.do("PUT", "/v1/instances/"+id+"/settings", tok, `{"auto_read":{"enabled":true}}`)
	ar, _ := set["auto_read"].(map[string]any)
	if code != 200 || ar["enabled"] != true || ar["groups"] != false {
		t.Fatalf("settings = %d %v", code, set)
	}
}
