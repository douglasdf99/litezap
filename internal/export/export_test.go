package export

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/douglasdf99/litezap/internal/store"
)

func setup(t *testing.T) (*Exporter, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "litezap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateInstance(ctx, store.Instance{ID: "inst_a", Name: "Loja", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	for id, owner := range map[string]string{"wh_c": "", "wh_op": store.OwnerOperator} {
		if err := st.CreateWebhook(ctx, store.Webhook{ID: id, URL: "https://x", Secret: "secret-" + id, Events: []string{"*"}, Version: 1, Enabled: true, Owner: owner}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetKV(ctx, "max_instances", "3")
	md := filepath.Join(dir, "media", "inst_a")
	if err := os.MkdirAll(md, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(md, "med_1"), []byte("JPEGDATA"), 0o600)
	_ = os.WriteFile(filepath.Join(md, "med_2-123.part"), []byte("partial"), 0o600)
	return &Exporter{Store: st, DataDir: dir, Version: "v-test"}, dir
}

func call(e *Exporter, sc service.Scope, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/export", strings.NewReader(body))
	req = req.WithContext(service.WithScope(req.Context(), sc))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func untar(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
	return out
}

func TestExportDownload(t *testing.T) {
	e, dir := setup(t)
	rec := call(e, service.Scope{Admin: true}, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("export = %d %s", rec.Code, rec.Body)
	}
	files := untar(t, rec.Body)
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != Format || m.EngineVersion != "v-test" || len(m.Instances) != 1 || m.Instances[0].Name != "Loja" || !m.IncludesMedia {
		t.Fatalf("manifest = %+v", m)
	}
	if string(files["media/inst_a/med_1"]) != "JPEGDATA" {
		t.Fatalf("media missing: %v", keys(files))
	}
	if _, ok := files["media/inst_a/med_2-123.part"]; ok {
		t.Fatal("partial download exported")
	}
	if bytes.Contains(files["litezap.db"], []byte("secret-wh_op")) {
		t.Fatal("operator webhook secret leaked into the export")
	}
	p := filepath.Join(t.TempDir(), "copy.db")
	_ = os.WriteFile(p, files["litezap.db"], 0o600)
	cp, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	hooks, _ := cp.ListWebhooks(context.Background())
	if len(hooks) != 1 || hooks[0].ID != "wh_c" {
		t.Fatalf("exported webhooks = %+v", hooks)
	}
	if _, err := cp.GetKV(context.Background(), "max_instances"); err == nil {
		t.Fatal("engine_kv exported")
	}
	if n, _ := cp.CountInstances(context.Background()); n != 1 {
		t.Fatalf("instances = %d", n)
	}
	// Temporary files are gone.
	if left, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	// Without media.
	rec = call(e, service.Scope{Admin: true}, `{"include_media":false}`)
	files = untar(t, rec.Body)
	if _, ok := files["media/inst_a/med_1"]; ok || len(files) != 2 {
		t.Fatalf("include_media=false exported %v", keys(files))
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExportUploadIsOperatorOnly(t *testing.T) {
	e, _ := setup(t)
	var got []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.ContentLength <= 0 {
			w.WriteHeader(400)
			return
		}
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	e.Client = srv.Client()
	body := `{"upload_url":"` + srv.URL + `/bucket/obj?X-Amz-Signature=abc"}`
	if rec := call(e, service.Scope{Admin: true}, body); rec.Code != 403 {
		t.Fatalf("admin upload = %d", rec.Code)
	}
	if rec := call(e, service.Scope{Admin: true, Operator: true}, `{"upload_url":"http://example.com/x"}`); rec.Code != 400 {
		t.Fatalf("http upload url = %d", rec.Code)
	}
	rec := call(e, service.Scope{Admin: true, Operator: true}, body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"uploaded":true`) {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	if files := untar(t, bytes.NewReader(got)); files["manifest.json"] == nil {
		t.Fatal("uploaded archive has no manifest")
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	if rec := call(e, service.Scope{Admin: true, Operator: true}, body); rec.Code != 502 || strings.Contains(rec.Body.String(), "X-Amz-Signature") {
		t.Fatalf("failed upload = %d %s", rec.Code, rec.Body)
	}
}

func TestOneExportAtATime(t *testing.T) {
	e, _ := setup(t)
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec := call(e, service.Scope{Admin: true}, ""); rec.Code != 409 {
		t.Fatalf("concurrent export = %d", rec.Code)
	}
}
