package localai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOllama serves /api/version, /api/tags and a streamed /api/pull.
type fakeOllama struct {
	mu     sync.Mutex
	models []string
	pulled []string
	gate   chan struct{} // closed to let the pull finish
	fail   string        // an error line in the stream
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"0.12.3"}`)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var list []map[string]any
		for _, m := range f.models {
			list = append(list, map[string]any{"name": m, "size": 4683087332, "details": map[string]any{"family": "qwen2", "parameter_size": "7.6B"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.pulled = append(f.pulled, b.Model)
		f.mu.Unlock()
		fl := w.(http.Flusher)
		line := func(s string) { _, _ = io.WriteString(w, s+"\n"); fl.Flush() }
		line(`{"status":"pulling manifest"}`)
		line(`{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":250}`)
		line(`{"status":"pulling bbb","digest":"sha256:bbb","total":1000,"completed":0}`)
		if f.fail != "" {
			line(`{"error":"` + f.fail + `"}`)
			return
		}
		select {
		case <-f.gate:
		case <-r.Context().Done():
			return
		}
		line(`{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":1000}`)
		line(`{"status":"verifying sha256 digest"}`)
		line(`{"status":"success"}`)
		f.mu.Lock()
		f.models = append(f.models, b.Model)
		f.mu.Unlock()
	})
	return mux
}

func wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStatusAndPull(t *testing.T) {
	f := &fakeOllama{models: []string{"llama3.2:3b"}, gate: make(chan struct{})}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	s, err := NewAt(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.RAM = func() uint64 { return 16 << 30 }
	ctx := context.Background()
	st := s.Status(ctx)
	if !st.Reachable || st.Version != "0.12.3" || len(st.Models) != 1 || st.Models[0].ParameterSize != "7.6B" ||
		st.Recommended.Model != "qwen2.5:7b" || st.RecommendedInstalled || st.BaseURL != srv.URL+"/v1" || st.DownloadURL != DownloadURL {
		t.Fatalf("status %+v", st)
	}
	if _, err := s.StartPull(ctx, "https://evil.example/model"); !errors.Is(err, ErrBadModel) {
		t.Fatalf("url accepted as a model: %v", err)
	}
	p, err := s.StartPull(ctx, "qwen2.5:7b")
	if err != nil || p.Model != "qwen2.5:7b" || p.Done {
		t.Fatalf("start %+v %v", p, err)
	}
	if _, err := s.StartPull(ctx, "llama3.2:3b"); !errors.Is(err, ErrPullRunning) {
		t.Fatalf("second pull: %v", err)
	}
	wait(t, "progress", func() bool { p := s.PullStatus(); return p != nil && p.Total == 2000 && p.Completed == 250 })
	if p := s.PullStatus(); p.Percent != 12.5 || p.Done || !strings.HasPrefix(p.Status, "pulling") {
		t.Fatalf("progress %+v", p)
	}
	close(f.gate)
	wait(t, "done", func() bool { p := s.PullStatus(); return p != nil && p.Done })
	if p := s.PullStatus(); !p.Success || p.Percent != 100 || p.Error != "" {
		t.Fatalf("done %+v", p)
	}
	if st := s.Status(ctx); !st.RecommendedInstalled || st.Pull == nil || !st.Pull.Success {
		t.Fatalf("after pull %+v", st)
	}
}

func TestPullErrorAndCancel(t *testing.T) {
	f := &fakeOllama{fail: "pull model manifest: file does not exist", gate: make(chan struct{})}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	s, _ := NewAt(srv.URL)
	ctx := context.Background()
	if _, err := s.StartPull(ctx, "naoexiste:1b"); err != nil {
		t.Fatal(err)
	}
	wait(t, "error", func() bool { p := s.PullStatus(); return p != nil && p.Done })
	if p := s.PullStatus(); p.Success || !strings.Contains(p.Error, "does not exist") {
		t.Fatalf("error %+v", p)
	}
	f.fail = ""
	if _, err := s.StartPull(ctx, "qwen2.5:7b"); err != nil {
		t.Fatal(err)
	}
	wait(t, "progress", func() bool { p := s.PullStatus(); return p != nil && p.Completed == 250 })
	s.CancelPull()
	wait(t, "cancel", func() bool { p := s.PullStatus(); return p != nil && p.Done })
	if p := s.PullStatus(); p.Success || !strings.Contains(p.Error, "cancel") {
		t.Fatalf("cancel %+v", p)
	}
}

func TestUnreachableAndLoopbackOnly(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	s, _ := NewAt(url)
	if st := s.Status(context.Background()); st.Reachable || st.Error == "" || st.Recommended.Model == "" {
		t.Fatalf("status %+v", st)
	}
	if _, err := s.StartPull(context.Background(), "qwen2.5:7b"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("pull: %v", err)
	}
	for _, bad := range []string{"http://192.168.0.10:11434", "http://ollama.example.com", "https://127.0.0.1:11434", "http://u:p@127.0.0.1:1", "http://127.0.0.1:1/x"} {
		if _, err := NewAt(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if s := New(); s.OpenAIBaseURL() != "http://127.0.0.1:11434/v1" {
		t.Fatal(s.OpenAIBaseURL())
	}
}

func TestRecommendAndNames(t *testing.T) {
	for ram, want := range map[uint64]string{0: "qwen2.5:7b", 8 << 30: "qwen2.5:7b", 16 << 30: "qwen2.5:7b", 4 << 30: "llama3.2:3b", 6 << 30: "llama3.2:3b"} {
		if got := Recommend(ram).Model; got != want {
			t.Errorf("ram %d: %s", ram, got)
		}
	}
	for name, ok := range map[string]bool{"qwen2.5:7b": true, "llama3.2": true, "library/qwen2.5:7b": true, "hf.co/x/y": false,
		"../etc": false, "a b": false, "": false, "http://x": false, "qwen2.5:7b-instruct-q4_K_M": true} {
		if ValidModel(name) != ok {
			t.Errorf("%q: %v", name, !ok)
		}
	}
	if !Installed([]Model{{Name: "llama3.2:latest"}}, "llama3.2") || Installed([]Model{{Name: "llama3.2:1b"}}, "llama3.2:3b") {
		t.Fatal("installed")
	}
	if n := parseMeminfo("MemTotal:       16303428 kB\nMemFree: 1 kB\n"); n != 16303428*1024 {
		t.Fatal(n)
	}
	if TotalRAM() == 0 {
		t.Log("RAM unknown on this system")
	}
}
