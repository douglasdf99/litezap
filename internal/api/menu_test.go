package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func testPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestMenuRoutes(t *testing.T) {
	e := newEnv(t)
	id, tok := e.connected("Pizzaria")
	_, otherTok := e.create("Outra")
	base := "/v1/instances/" + id
	errCode := func(out map[string]any) any {
		if m, ok := out["error"].(map[string]any); ok {
			return m["code"]
		}
		return nil
	}

	code, out := e.do("GET", base+"/menu", tok, "")
	if code != 200 || len(out["categories"].([]any)) != 0 || out["limits"].(map[string]any)["max_items"] != 500.0 {
		t.Fatalf("empty menu = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/categories", tok, `{"name":"Pizzas"}`)
	if code != 201 {
		t.Fatalf("category = %d %v", code, out)
	}
	cat := out["id"].(string)
	_, out = e.do("POST", base+"/menu/categories", tok, `{"name":"Bebidas"}`)
	drinks := out["id"].(string)
	code, out = e.do("POST", base+"/menu/items", tok, `{"category_id":"`+cat+`","name":"Calabresa","price":"45,90",
		"options":[{"name":"Tamanho","required":true,"choices":[{"name":"P"},{"name":"G","price_delta_cents":1000}]}]}`)
	if code != 201 || out["price_cents"] != 4590.0 || len(out["options"].([]any)) != 1 {
		t.Fatalf("item = %d %v", code, out)
	}
	item := out["id"].(string)
	if code, out := e.do("POST", base+"/menu/items", tok, `{"name":"x","price":"caro"}`); code != 400 || errCode(out) != "validation_failed" {
		t.Fatalf("bad price = %d %v", code, out)
	}
	if code, _ := e.do("GET", base+"/menu", otherTok, ""); code != 403 {
		t.Fatalf("other token = %d", code)
	}
	code, out = e.do("PATCH", base+"/menu/items/"+item, tok, `{"description":"Molho, calabresa e cebola"}`)
	if code != 200 || out["description"] != "Molho, calabresa e cebola" || out["price_cents"] != 4590.0 {
		t.Fatalf("patch = %d %v", code, out)
	}
	if code, out := e.do("PUT", base+"/menu/items/"+item+"/available", tok, `{"available":false}`); code != 200 || out["available"] != false {
		t.Fatalf("esgotado = %d %v", code, out)
	}
	e.do("PUT", base+"/menu/items/"+item+"/available", tok, `{"available":true}`)
	code, out = e.do("PUT", base+"/menu/items/"+item+"/image", tok, `{"base64":"`+testPNG(t)+`"}`)
	if code != 200 || !strings.Contains(out["image_path"].(string), "/menu/items/"+item+"/image") {
		t.Fatalf("image = %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+out["image_path"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || len(b) == 0 {
		t.Fatalf("get image = %d %v", resp.StatusCode, resp.Header)
	}
	code, out = e.do("POST", base+"/menu/items/"+item+"/options", tok, `{"name":"Borda","choices":[{"name":"Catupiry","price_delta_cents":800}]}`)
	if code != 201 {
		t.Fatalf("option = %d %v", code, out)
	}
	opt := out["id"].(string)
	if code, _ := e.do("DELETE", base+"/menu/items/"+item+"/options/"+opt, tok, ""); code != 204 {
		t.Fatalf("delete option = %d", code)
	}
	if code, _ := e.do("PUT", base+"/menu/categories/order", tok, `{"ids":["`+drinks+`","`+cat+`"]}`); code != 200 {
		t.Fatalf("reorder categories = %d", code)
	}
	code, out = e.do("PUT", base+"/menu/items/order", tok, `{"category_id":"`+drinks+`","ids":["`+item+`"]}`)
	if code != 200 || out["categories"].([]any)[0].(map[string]any)["name"] != "Bebidas" ||
		len(out["categories"].([]any)[0].(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("reorder items = %d %v", code, out)
	}
	if code, out := e.do("GET", base+"/menu/text", tok, ""); code != 200 || !strings.Contains(out["chunks"].([]any)[0].(string), "*Bebidas*") {
		t.Fatalf("text = %d %v", code, out)
	}

	// Sends go through the queue: the item (photo + caption) and the menu text.
	code, out = e.do("POST", base+"/menu/items/"+item+"/send", tok, `{"to":"5511988887777"}`)
	if (code != 200 && code != 202) || out["type"] != "image" {
		t.Fatalf("send item = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/messages", tok, `{"to":"5511988887777","type":"menu"}`)
	if code != 200 && code != 202 {
		t.Fatalf("send menu message = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/send", tok, `{"to":"5511988887777","client_message_id":"m1"}`)
	if code != 202 || len(out["messages"].([]any)) != 1 {
		t.Fatalf("menu/send = %d %v", code, out)
	}
	e.eventually("menu sends", func() bool {
		n := 0
		for _, r := range e.sp.Last(id).WA.Sent() {
			if strings.Contains(r.Text, "Calabresa") {
				n++
			}
		}
		return n == 3
	})

	// CSV preview and commit.
	csv := `categoria;nome;preco\nSobremesas;Pudim;\"12,50\"\nSobremesas;;abc\n`
	code, out = e.do("POST", base+"/menu/import/csv", tok, `{"csv":"`+csv+`","dry_run":true}`)
	if code != 200 || out["valid"] != 1.0 || out["invalid"] != 1.0 {
		t.Fatalf("csv preview = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/import/csv", tok, `{"csv":"`+csv+`"}`)
	if code != 400 || out["preview"] == nil {
		t.Fatalf("csv with errors = %d %v", code, out)
	}
	code, out = e.do("POST", base+"/menu/import/csv", tok, `{"csv":"`+csv+`","skip_invalid":true}`)
	if code != 201 || out["items_created"] != 1.0 {
		t.Fatalf("csv commit = %d %v", code, out)
	}
	if code, out := e.do("POST", base+"/menu/import/ai", tok, `{"text":"pizza 45"}`); code != 501 {
		t.Fatalf("ai without provider = %d %v", code, out)
	}

	// Scoped API keys: read keys read the menu but cannot edit it; send keys can send it.
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"bi","scopes":["read"]}`)
	readKey := out["key"].(string)
	if code, _ := e.do("GET", base+"/menu", readKey, ""); code != 200 {
		t.Fatalf("read key GET /menu = %d", code)
	}
	if code, _ := e.do("GET", base+"/menu/items/"+item, readKey, ""); code != 200 {
		t.Fatalf("read key GET item = %d", code)
	}
	for _, c := range [][3]string{{"POST", "/menu/items", `{"name":"x"}`}, {"PATCH", "/menu/items/" + item, `{"name":"x"}`},
		{"DELETE", "/menu/items/" + item, ""}, {"POST", "/menu/import/csv", `{"csv":"nome\nx\n"}`}, {"POST", "/menu/send", `{"to":"5511988887777"}`}} {
		if code, _ := e.do(c[0], base+c[1], readKey, c[2]); code != 403 {
			t.Fatalf("read key %s %s = %d", c[0], c[1], code)
		}
	}
	_, out = e.do("POST", base+"/api-keys", tok, `{"name":"crm","scopes":["send"]}`)
	sendKey := out["key"].(string)
	if code, _ := e.do("POST", base+"/menu/items/"+item+"/send", sendKey, `{"to":"5511988887777"}`); code != 200 && code != 202 {
		t.Fatalf("send key item send = %d", code)
	}
	if code, _ := e.do("PUT", base+"/menu/items/"+item+"/available", sendKey, `{"available":false}`); code != 403 {
		t.Fatalf("send key edited the menu = %d", code)
	}

	if code, _ := e.do("DELETE", base+"/menu/categories/"+cat, tok, ""); code != 204 {
		t.Fatalf("delete category = %d", code)
	}
	if code, _ := e.do("DELETE", base+"/menu/items/"+item, tok, ""); code != 204 {
		t.Fatalf("delete item = %d", code)
	}
	if code, _ := e.do("GET", base+"/menu/items/"+item, tok, ""); code != 404 {
		t.Fatalf("deleted item = %d", code)
	}
}
