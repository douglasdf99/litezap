package media

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.0.10", "172.16.0.1", "169.254.169.254",
		"::1", "fe80::1", "fd00::1", "0.0.0.0", "0.1.2.3", "100.64.0.1", "::ffff:127.0.0.1", "198.18.0.1",
		"240.0.0.1", "255.255.255.255", "224.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "157.240.1.1", "2a03:2880::1"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFetchBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("x")) }))
	defer srv.Close()
	if _, _, err := (&Fetcher{}).Fetch(context.Background(), srv.URL, 100); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

// A URL on an allowed host that redirects to an internal one is blocked on the second hop.
func TestFetchBlocksRedirectToInternalHost(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://internal.test:"+srv.URL[strings.LastIndex(srv.URL, ":")+1:]+"/secret", http.StatusFound)
			return
		}
		w.Write([]byte("secret"))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	f := &Fetcher{
		LookupIP: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil // both names point to the test server
		},
		allow: func(host string, _ netip.Addr) bool { return host != "internal.test" },
	}
	if data, _, err := f.Fetch(context.Background(), "http://public.test:"+port+"/secret", 100); err != nil || string(data) != "secret" {
		t.Fatalf("allowed host = %q, %v", data, err)
	}
	if _, _, err := f.Fetch(context.Background(), "http://public.test:"+port+"/redir", 100); !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect to internal host = %v, want ErrBlocked", err)
	}
}

func TestFetchWhenPrivateAllowed(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img":
			w.Header().Set("Content-Type", "text/html") // lies; ResolveMime ignores it
			w.Write(png)
		case "/big":
			w.Write(make([]byte, 200))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &Fetcher{AllowPrivate: true}
	data, remote, err := f.Fetch(context.Background(), srv.URL+"/img", 100)
	if err != nil || string(data) != string(png) || remote != "text/html" {
		t.Fatalf("fetch = %q %q %v", data, remote, err)
	}
	if _, _, err := f.Fetch(context.Background(), srv.URL+"/big", 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big = %v", err)
	}
	if _, _, err := f.Fetch(context.Background(), srv.URL+"/missing", 100); !errors.Is(err, ErrFetch) {
		t.Fatalf("missing = %v", err)
	}
	for _, bad := range []string{"ftp://example.com/x", "http:///nohost", "::nope"} {
		if _, _, err := f.Fetch(context.Background(), bad, 100); !errors.Is(err, ErrInvalidURL) || errors.Is(err, ErrFetch) {
			t.Fatalf("invalid url %q = %v", bad, err)
		}
	}
}

func TestResolveMime(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	ogg := []byte("OggS\x00\x02rest-of-ogg")
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	cases := []struct {
		kind, declared, remote string
		data                   []byte
		want                   string
		bad                    bool
	}{
		{"image", "", "image/webp", png, "image/png", false},          // images take the sniffed type
		{"image", "", "text/html", png, "image/png", false},           // contradicting remote type ignored
		{"image", "image/jpeg", "image/png", png, "image/png", false}, // even over a declared one
		{"image", "", "image/svg+xml", []byte("<svg/>"), "", true},    // never accept SVG as an image
		{"audio", "", "", ogg, "audio/ogg", false},
		{"audio", "audio/ogg; codecs=opus", "", ogg, "audio/ogg", false},
		{"video", "", "", []byte("plain text"), "", true},
		{"document", "", "application/pdf", []byte("%PDF"), "application/pdf", false},
		{"document", "", "", []byte{0, 1, 2}, "application/octet-stream", false},
		// The bytes are always checked: a declared or remote type does not make HTML media.
		{"image", "image/png", "", html, "", true},
		{"image", "", "image/png", html, "", true},
		{"video", "video/mp4", "", html, "", true},
		{"audio", "", "audio/mpeg", html, "", true},
		{"video", "video/mp4", "", []byte("%PDF-1.4 rest"), "", true},
		{"audio", "audio/ogg", "", []byte("PK\x03\x04rest-of-zip"), "", true},
		{"audio", "audio/mpeg", "", []byte("\x1f\x8b\x08rest-of-gzip"), "", true},
		// Audio/video the sniffer does not know (e.g. AMR) keeps the declared type.
		{"audio", "audio/amr", "", []byte("#!AMR\n\x00\x01"), "audio/amr", false},
	}
	for i, c := range cases {
		got, err := ResolveMime(c.kind, c.declared, c.remote, c.data)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("case %d: %q, %v", i, got, err)
		}
	}
}

func TestDecodeBase64(t *testing.T) {
	raw := []byte("%PDF-1.4 hello")
	enc := base64.StdEncoding.EncodeToString(raw)
	data, mime, err := DecodeBase64(enc, 100)
	if err != nil || string(data) != string(raw) || mime != "" {
		t.Fatalf("plain = %q %q %v", data, mime, err)
	}
	data, mime, err = DecodeBase64("data:image/webp;base64,"+enc, 100)
	if err != nil || mime != "image/webp" || len(data) != len(raw) {
		t.Fatalf("data url = %q %v", mime, err)
	}
	if _, _, err := DecodeBase64(enc, 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large = %v", err)
	}
	if _, _, err := DecodeBase64("!!!", 100); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}

func TestKeyAndSigner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media_key")
	key, err := LoadOrCreateKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key = %x, %v", key, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", fi.Mode().Perm())
	}
	again, err := LoadOrCreateKey(path)
	if err != nil || string(again) != string(key) {
		t.Fatal("key not reused")
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := NewSigner(key, "http://127.0.0.1:8787/")
	s.Now = func() time.Time { return now }
	raw, exp := s.URL("inst_1", "med_1")
	if !exp.Equal(now.Add(time.Hour)) || !strings.HasPrefix(raw, "http://127.0.0.1:8787/media/inst_1/med_1?") {
		t.Fatalf("url = %s exp = %v", raw, exp)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if !s.Verify("inst_1", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("valid signature rejected")
	}
	if s.Verify("inst_1", "med_2", q.Get("exp"), q.Get("sig")) || s.Verify("inst_2", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("signature not bound to the media")
	}
	s.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if s.Verify("inst_1", "med_1", q.Get("exp"), q.Get("sig")) {
		t.Fatal("expired URL accepted")
	}
	if MaxSize("image") != 16<<20 || MaxSize("document") != 100<<20 || MaxSize("audio") != 64<<20 {
		t.Fatal("limits")
	}
}

// Concurrent first starts agree on one key, and no temporary file is left behind.
func TestKeyConcurrentCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media_key")
	const n = 16
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = LoadOrCreateKey(path)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || string(keys[i]) != string(keys[0]) {
			t.Fatalf("call %d: key %x, %v (first %x)", i, keys[i], errs[i], keys[0])
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// Without hard links (FAT, some SMB/FUSE mounts) the key is created with O_EXCL instead.
func TestKeyWithoutHardLinks(t *testing.T) {
	orig := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errors.ErrUnsupported}
	}
	t.Cleanup(func() { linkFile = orig })
	dir := t.TempDir()
	path := filepath.Join(dir, "media_key")
	key, err := LoadOrCreateKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key = %x, %v", key, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	if again, err := LoadOrCreateKey(path); err != nil || string(again) != string(key) {
		t.Fatalf("key not reused: %x, %v", again, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files left: %v", entries)
	}
}

// The MAC input cannot be shifted between instance and media ids.
func TestSignatureIsUnambiguous(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "http://127.0.0.1:8787")
	raw, _ := s.URL("a/b", "c")
	u, _ := url.Parse(raw)
	q := u.Query()
	if !s.Verify("a/b", "c", q.Get("exp"), q.Get("sig")) {
		t.Fatal("valid signature rejected")
	}
	if s.Verify("a", "b/c", q.Get("exp"), q.Get("sig")) {
		t.Fatal("signature of a/b + c accepted for a + b/c")
	}
}

func TestSafeTransportBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := (&http.Client{Transport: SafeTransport(false)}).Get(srv.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback dial: %v, want ErrBlocked", err)
	}
	resp, err := (&http.Client{Transport: SafeTransport(true)}).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowed private: %v", err)
	}
	resp.Body.Close()
}

// Hosts such as Wikimedia refuse Go's default User-Agent, so every download identifies LiteZap.
func TestFetchSendsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("\xff\xd8\xff"))
	}))
	defer srv.Close()
	if _, _, err := (&Fetcher{AllowPrivate: true}).Fetch(context.Background(), srv.URL, 100); err != nil {
		t.Fatal(err)
	}
	if got != MediaUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, MediaUserAgent)
	}
}
