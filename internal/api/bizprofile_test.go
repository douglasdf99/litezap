package api

import (
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestPatchBusinessProfile(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Biz")
	_, otherTok := e.create("Outra")
	base := "/v1/instances/" + id
	f := e.sp.Last(id)
	body := `{"description":"Pizza artesanal","websites":["https://loja.x"],"categories":["7"],
		"hours":{"timezone":"America/Sao_Paulo","days":{"mon":{"mode":"open","open":"09:00","close":"18:00"},"sat":{"mode":"24h"}}}}`

	if code, out := e.do("PATCH", base+"/business/profile", tok, body); code != 409 || out["error"].(map[string]any)["code"] != "business_account_required" {
		t.Fatalf("personal account = %d %v", code, out)
	}
	f.WA.SetBusiness(true, &workerv1.BusinessProfileInfo{Jid: "5511999999999@s.whatsapp.net", Email: "oi@loja.x"})
	if code, _ := e.do("PATCH", base+"/business/profile", otherTok, body); code != 403 {
		t.Fatalf("other instance token = %d", code)
	}
	for _, bad := range []string{`{}`, `{"descripton":"typo"}`, `{"latitude":1}`, `{"websites":["ftp://x"]}`,
		`{"hours":{"timezone":"America/Sao_Paulo","days":{"mon":{"mode":"open","open":"9","close":"18:00"}}}}`} {
		if code, out := e.do("PATCH", base+"/business/profile", tok, bad); code != 400 {
			t.Errorf("%s = %d %v", bad, code, out)
		}
	}
	sse, _ := e.sse(base+"/events?types=business.profile_updated", tok)
	code, out := e.do("PATCH", base+"/business/profile", tok, body)
	if code != 200 || out["description"] != "Pizza artesanal" || out["email"] != "oi@loja.x" {
		t.Fatalf("patch = %d %v", code, out)
	}
	days := out["business_hours"].(map[string]any)["days"].(map[string]any)
	if days["mon"].(map[string]any)["open"] != "09:00" || days["sat"].(map[string]any)["mode"] != "24h" || days["sun"].(map[string]any)["mode"] != "closed" {
		t.Fatalf("business_hours = %v", days)
	}
	if ev := sse.next("business.profile_updated"); ev.data == "" {
		t.Fatal("no business.profile_updated")
	}
	// The admin token may change it too.
	if code, out := e.do("PATCH", base+"/business/profile", admin, `{"address":"Rua A, 10"}`); code != 200 || out["address"] != "Rua A, 10" {
		t.Fatalf("admin patch = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/business/profile", tok, ""); code != 200 || out["address"] != "Rua A, 10" ||
		out["category_list"].([]any)[0].(map[string]any)["id"] != "7" {
		t.Fatalf("get = %d %v", code, out)
	}
	if n := len(f.WA.BusinessSets()); n != 2 {
		t.Fatalf("worker sets = %d", n)
	}
}
