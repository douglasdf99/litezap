package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMenuCatalogRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Pizzaria")
	base := "/v1/instances/" + id
	errCode := func(out map[string]any) any {
		if m, ok := out["error"].(map[string]any); ok {
			return m["code"]
		}
		return nil
	}
	code, out := e.do("POST", base+"/menu/items", tok, `{"name":"=Calabresa","price":"50","sale_price":"39,90","sale_until":"31/12/2099",
		"link_url":"https://loja.example/cal","sku":"CAL","tags":["picante"],
		"options":[{"name":"Tamanho","required":true,"choices":[{"name":"P"},{"name":"G","price_delta_cents":1000}]}]}`)
	if code != 201 || out["sale_price_cents"] != 3990.0 || out["price_label"] != "~R$ 50,00~ *R$ 39,90*" || out["on_sale"] != true {
		t.Fatalf("item = %d %v", code, out)
	}
	item := out["id"].(string)
	if code, out := e.do("POST", base+"/menu/items", tok, `{"name":"x","price":"10","sale_price":"12"}`); code != 400 || errCode(out) != "validation_failed" {
		t.Fatalf("sale over price = %d %v", code, out)
	}
	_, out = e.do("POST", base+"/menu/items", tok, `{"name":"Coca","price":"12"}`)
	coca := out["id"].(string)
	// Photos: add, list, make main, remove.
	code, out = e.do("POST", base+"/menu/items/"+item+"/images", tok, `{"base64":"`+testPNG(t)+`"}`)
	if code != 201 || len(out["images"].([]any)) != 1 {
		t.Fatalf("add photo = %d %v", code, out)
	}
	_, out = e.do("POST", base+"/menu/items/"+item+"/images", tok, `{"base64":"`+testPNG(t)+`","main":true}`)
	imgs := out["images"].([]any)
	if len(imgs) != 2 {
		t.Fatalf("photos = %v", out)
	}
	second := imgs[1].(map[string]any)
	req, _ := http.NewRequest("GET", e.srv.URL+second["path"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("photo by id = %d", resp.StatusCode)
	}
	if code, out := e.do("PUT", base+"/menu/items/"+item+"/images/"+second["id"].(string)+"/main", tok, ""); code != 200 ||
		out["images"].([]any)[0].(map[string]any)["id"] != second["id"] {
		t.Fatalf("main = %d %v", code, out)
	}
	if code, out := e.do("DELETE", base+"/menu/items/"+item+"/images/"+second["id"].(string), tok, ""); code != 200 || len(out["images"].([]any)) != 1 {
		t.Fatalf("remove photo = %d %v", code, out)
	}
	// Hidden and search.
	if code, out := e.do("PUT", base+"/menu/items/"+coca+"/hidden", tok, `{"hidden":true}`); code != 200 || out["hidden"] != true {
		t.Fatalf("hidden = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu?include_hidden=false", tok, ""); code != 200 || out["item_count"] != 1.0 {
		t.Fatalf("include_hidden=false = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/items?hidden=true", tok, ""); code != 200 || out["total"] != 1.0 {
		t.Fatalf("search hidden = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/items?q=calabresa&on_sale=true", tok, ""); code != 200 || out["total"] != 1.0 {
		t.Fatalf("search = %d %v", code, out)
	}
	if code, _ := e.do("GET", base+"/menu/items?available=talvez", tok, ""); code != 400 {
		t.Fatalf("bad bool = %d", code)
	}
	// Duplicate and bulk (dry run, then a refused one keeps its preview).
	code, out = e.do("POST", base+"/menu/items/"+item+"/duplicate", tok, `{"name":"Calabresa G"}`)
	if code != 201 || out["name"] != "Calabresa G" || len(out["images"].([]any)) != 1 {
		t.Fatalf("duplicate = %d %v", code, out)
	}
	dup := out["id"].(string)
	code, out = e.do("POST", base+"/menu/items/bulk", tok, `{"item_ids":["`+item+`","`+dup+`"],"action":"adjust_price","percent":10,"dry_run":true}`)
	if code != 200 || out["dry_run"] != true || out["affected"] != 2.0 {
		t.Fatalf("bulk preview = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/items/bulk", tok, `{"item_ids":["`+item+`"],"action":"adjust_price","amount_cents":-4500}`)
	if code != 400 || out["preview"] == nil {
		t.Fatalf("refused bulk = %d %v", code, out)
	}
	// Collections.
	code, out = e.do("POST", base+"/menu/collections", tok, `{"name":"Promoções","item_ids":["`+item+`"],"cover_item_id":"`+item+`"}`)
	if code != 201 || out["item_count"] != 1.0 || out["cover_path"] == "" {
		t.Fatalf("collection = %d %v", code, out)
	}
	col := out["id"].(string)
	if code, out := e.do("POST", base+"/menu/collections/"+col+"/items", tok, `{"item_ids":["`+dup+`"]}`); code != 200 || out["item_count"] != 2.0 {
		t.Fatalf("add items = %d %v", code, out)
	}
	if code, out := e.do("PUT", base+"/menu/collections/"+col+"/items/order", tok, `{"ids":["`+dup+`","`+item+`"]}`); code != 200 ||
		out["item_ids"].([]any)[0] != dup {
		t.Fatalf("reorder items = %d %v", code, out)
	}
	if code, out := e.do("POST", base+"/menu/collections/"+col+"/items/remove", tok, `{"item_ids":["`+dup+`"]}`); code != 200 || out["item_count"] != 1.0 {
		t.Fatalf("remove items = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/collections/"+col+"/text", tok, ""); code != 200 || !strings.HasPrefix(out["chunks"].([]any)[0].(string), "*Promoções*") {
		t.Fatalf("text = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/text?collection_id="+col, tok, ""); code != 200 || len(out["chunks"].([]any)) != 1 {
		t.Fatalf("menu text of a collection = %d %v", code, out)
	}
	if code, out := e.do("POST", base+"/menu/collections/"+col+"/send", tok, `{"to":"5511988887777","photos":1}`); code != 202 || len(out["messages"].([]any)) != 2 {
		t.Fatalf("send collection = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/collections", tok, ""); code != 200 || len(out["collections"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, out := e.do("PATCH", base+"/menu/collections/"+col, tok, `{"active":false}`); code != 200 || out["active"] != false {
		t.Fatalf("patch = %d %v", code, out)
	}
	// Order drafts: preview, refused cart (preview in the body), save, send, status.
	cart := `{"chat":"5511988887777","items":[{"item_id":"` + item + `","quantity":2,"options":["G"]}]`
	if code, out := e.do("POST", base+"/menu/orders", tok, cart+`,"dry_run":true}`); code != 200 || out["total_cents"] != 9980.0 || out["id"] != nil {
		t.Fatalf("preview = %d %v", code, out)
	}
	if code, out := e.do("POST", base+"/menu/orders", tok, `{"items":[{"item_id":"`+item+`","quantity":1}]}`); code != 400 || out["preview"] == nil {
		t.Fatalf("missing option = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/orders", tok, cart+`}`)
	if code != 201 || out["status"] != "draft" {
		t.Fatalf("order = %d %v", code, out)
	}
	ord := out["id"].(string)
	if code, out := e.do("POST", base+"/menu/orders/"+ord+"/send", tok, ""); code != 202 || out["draft"].(map[string]any)["status"] != "sent" {
		t.Fatalf("send order = %d %v", code, out)
	}
	if code, out := e.do("PATCH", base+"/menu/orders/"+ord, tok, `{"status":"confirmed"}`); code != 200 || out["status"] != "confirmed" {
		t.Fatalf("status = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/orders?status=confirmed", tok, ""); code != 200 || len(out["drafts"].([]any)) != 1 {
		t.Fatalf("list orders = %d %v", code, out)
	}
	if code, _ := e.do("GET", base+"/menu/orders/"+ord, tok, ""); code != 200 {
		t.Fatalf("get order = %d", code)
	}
	// CSV export: a download, escaped against formula injection.
	req, _ = http.NewRequest("GET", e.srv.URL+base+"/menu/export.csv", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "cardapio.csv") || !strings.Contains(string(body), "\n;'=Calabresa;") {
		t.Fatalf("export = %d %v %q", resp.StatusCode, resp.Header, body)
	}
	// Scoped keys: read keys read collections, orders and the export; send keys send them.
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read"]}`)
	readKey := out["key"].(string)
	for _, p := range []string{"/menu/collections", "/menu/collections/" + col, "/menu/orders", "/menu/orders/" + ord, "/menu/items?q=x", "/menu/export.csv"} {
		if code, _ := e.do("GET", base+p, readKey, ""); code != 200 {
			t.Fatalf("read key GET %s = %d", p, code)
		}
	}
	for _, c := range [][3]string{{"POST", "/menu/collections", `{"name":"x"}`}, {"POST", "/menu/orders", cart + `}`},
		{"POST", "/menu/items/bulk", `{}`}, {"POST", "/menu/collections/" + col + "/send", `{"to":"5511988887777"}`}} {
		if code, _ := e.do(c[0], base+c[1], readKey, c[2]); code != 403 {
			t.Fatalf("read key %s %s = %d", c[0], c[1], code)
		}
	}
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"crm","scopes":["send"]}`)
	sendKey := out["key"].(string)
	e.do("PATCH", base+"/menu/collections/"+col, tok, `{"active":true}`)
	if code, out := e.do("POST", base+"/menu/collections/"+col+"/send", sendKey, `{"to":"5511988887777"}`); code != 202 {
		t.Fatalf("send key collection send = %d %v", code, out)
	}
	if code, _ := e.do("POST", base+"/menu/orders", sendKey, cart+`}`); code != 403 {
		t.Fatalf("send key created an order = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/menu/collections/"+col, tok, ""); code != 204 {
		t.Fatalf("delete collection = %d", code)
	}
}
