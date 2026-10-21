// Package localai detects a local Ollama server and downloads models into it, so the
// built-in attendant and the agents can run for free on the user's own computer.
//
// Only the loopback Ollama is ever contacted (http://127.0.0.1:11434 by default): the
// address is fixed by the engine, never taken from a request. Model names are checked
// against a strict pattern (no registry hosts or URLs).
package localai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the loopback Ollama server.
const DefaultURL = "http://127.0.0.1:11434"

// DownloadURL is where the user installs Ollama (LiteZap never installs it).
const DownloadURL = "https://ollama.com/download"

// ModelOption is a model LiteZap recommends for a kind of machine.
type ModelOption struct {
	Model string `json:"model"`
	// SizeBytes is the download size (approximate); SizeLabel is it in Portuguese.
	SizeBytes int64  `json:"size_bytes"`
	SizeLabel string `json:"size_label"`
	// MinRAMBytes is the RAM below which the model is too heavy.
	MinRAMBytes uint64 `json:"min_ram_bytes"`
	Note        string `json:"note"`
}

const gib = 1 << 30

// Options are the recommended models, best first (both support tool calling on Ollama).
var Options = []ModelOption{
	{Model: "qwen2.5:7b", SizeBytes: 4_700_000_000, SizeLabel: "4,7 GB", MinRAMBytes: 15 * gib / 2,
		Note: "Recomendado: bom em português e no uso de ferramentas. Precisa de 8 GB de RAM ou mais."},
	{Model: "llama3.2:3b", SizeBytes: 2_000_000_000, SizeLabel: "2,0 GB", MinRAMBytes: 0,
		Note: "Para computadores fracos (4 a 8 GB de RAM): mais rápido, porém erra mais."},
}

// Recommend picks the model for a machine with this much RAM (0 = unknown: the default).
func Recommend(ram uint64) ModelOption {
	for _, o := range Options {
		if ram == 0 || ram >= o.MinRAMBytes {
			return o
		}
	}
	return Options[len(Options)-1]
}

// Model is a model installed in Ollama.
type Model struct {
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Family        string `json:"family,omitempty"`
}

// Status is what GET /v1/ai/local reports.
type Status struct {
	// Reachable: Ollama answered on the loopback address.
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
	// BaseURL is the OpenAI-compatible base URL to configure (the ollama preset's).
	BaseURL string  `json:"base_url"`
	Models  []Model `json:"models"`
	// RAMBytes is the machine's memory (0 = unknown).
	RAMBytes    uint64        `json:"ram_bytes"`
	Recommended ModelOption   `json:"recommended"`
	Options     []ModelOption `json:"options"`
	// RecommendedInstalled: the recommended model is already downloaded.
	RecommendedInstalled bool        `json:"recommended_installed"`
	DownloadURL          string      `json:"download_url"`
	Pull                 *PullStatus `json:"pull,omitempty"`
}

// PullStatus is the progress of a model download.
type PullStatus struct {
	Model     string    `json:"model"`
	Status    string    `json:"status"` // Ollama's last status line
	Total     int64     `json:"total_bytes"`
	Completed int64     `json:"completed_bytes"`
	Percent   float64   `json:"percent"`
	Done      bool      `json:"done"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Errors of StartPull.
var (
	ErrBadModel    = errors.New("invalid model name: use a name like qwen2.5:7b or llama3.2:3b")
	ErrPullRunning = errors.New("a model download is already in progress")
	ErrUnreachable = errors.New("Ollama is not running on this computer (127.0.0.1:11434): install it from " + DownloadURL + " and open it")
)

// modelRe accepts Ollama library names: name, namespace/name, with an optional tag. No
// dots in the first segment of a namespaced name, so no registry host (hf.co/...).
var modelRe = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9_-]{0,63}/)?[a-z0-9][a-z0-9._-]{0,79}(?::[A-Za-z0-9][A-Za-z0-9._-]{0,63})?$`)

// ValidModel reports a model name LiteZap accepts to download.
func ValidModel(name string) bool { return len(name) <= 150 && modelRe.MatchString(name) }

// Service talks to the loopback Ollama.
type Service struct {
	base   string
	client *http.Client
	// RAM returns the machine's memory (tests replace it).
	RAM func() uint64

	mu     sync.Mutex
	pull   *PullStatus
	cancel context.CancelFunc
}

// New returns the service for the default loopback Ollama.
func New() *Service {
	s, err := NewAt(DefaultURL)
	if err != nil {
		panic(err)
	}
	return s
}

// NewAt returns the service for an Ollama at base, which must be a loopback http URL
// (tests use an httptest server on 127.0.0.1).
func NewAt(base string) (*Service, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" && u.Path != "/" {
		return nil, errors.New("localai: base must be http://127.0.0.1:port")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("localai: only a loopback Ollama is allowed")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // never through a proxy: the server is on this machine
	return &Service{base: strings.TrimRight(base, "/"), RAM: TotalRAM,
		client: &http.Client{Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// OpenAIBaseURL is the OpenAI-compatible base URL of this Ollama.
func (s *Service) OpenAIBaseURL() string { return s.base + "/v1" }

func (s *Service) getJSON(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// Status checks Ollama: version, installed models and the recommendation for this machine.
func (s *Service) Status(ctx context.Context) Status {
	ram := uint64(0)
	if s.RAM != nil {
		ram = s.RAM()
	}
	st := Status{BaseURL: s.OpenAIBaseURL(), Models: []Model{}, RAMBytes: ram, Recommended: Recommend(ram),
		Options: append([]ModelOption(nil), Options...), DownloadURL: DownloadURL, Pull: s.PullStatus()}
	var ver struct {
		Version string `json:"version"`
	}
	if err := s.getJSON(ctx, "/api/version", &ver); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Reachable, st.Version = true, ver.Version
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Details struct {
				Family        string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := s.getJSON(ctx, "/api/tags", &tags); err != nil {
		st.Error = err.Error()
		return st
	}
	for _, m := range tags.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		st.Models = append(st.Models, Model{Name: name, SizeBytes: m.Size, ParameterSize: m.Details.ParameterSize, Family: m.Details.Family})
	}
	st.RecommendedInstalled = Installed(st.Models, st.Recommended.Model)
	return st
}

// Installed reports whether model is in the list (a name without a tag is :latest).
func Installed(models []Model, model string) bool {
	norm := func(n string) string {
		if !strings.Contains(n, ":") {
			return n + ":latest"
		}
		return n
	}
	for _, m := range models {
		if norm(m.Name) == norm(model) {
			return true
		}
	}
	return false
}

// PullStatus returns a copy of the current (or last) download, nil when none.
func (s *Service) PullStatus() *PullStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pull == nil {
		return nil
	}
	p := *s.pull
	return &p
}

// StartPull starts downloading a model in the background (one at a time); its progress
// is in PullStatus.
func (s *Service) StartPull(ctx context.Context, model string) (PullStatus, error) {
	model = strings.TrimSpace(model)
	if !ValidModel(model) {
		return PullStatus{}, ErrBadModel
	}
	var ver struct {
		Version string `json:"version"`
	}
	if err := s.getJSON(ctx, "/api/version", &ver); err != nil {
		return PullStatus{}, ErrUnreachable
	}
	s.mu.Lock()
	if s.pull != nil && !s.pull.Done {
		s.mu.Unlock()
		return PullStatus{}, ErrPullRunning
	}
	now := time.Now()
	s.pull = &PullStatus{Model: model, Status: "starting", StartedAt: now, UpdatedAt: now}
	pctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	s.cancel = cancel
	p := *s.pull
	s.mu.Unlock()
	go s.runPull(pctx, cancel, model)
	return p, nil
}

// CancelPull stops the download in progress (Ollama keeps the downloaded parts).
func (s *Service) CancelPull() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil && s.pull != nil && !s.pull.Done {
		s.cancel()
	}
}

func (s *Service) update(f func(p *PullStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pull != nil {
		f(s.pull)
		s.pull.UpdatedAt = time.Now()
		if s.pull.Total > 0 {
			s.pull.Percent = float64(int64(float64(s.pull.Completed)*1000/float64(s.pull.Total))) / 10
		}
	}
}

func (s *Service) finish(errText string) {
	s.update(func(p *PullStatus) {
		p.Done = true
		if errText != "" {
			p.Error = errText
			return
		}
		p.Success, p.Status, p.Percent = true, "success", 100
		if p.Total > 0 {
			p.Completed = p.Total
		}
	})
}

// runPull streams POST /api/pull (NDJSON progress lines) into the status.
func (s *Service) runPull(ctx context.Context, cancel context.CancelFunc, model string) {
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/pull", bytes.NewReader(body))
	if err != nil {
		s.finish("could not start the download")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			s.finish("download cancelled")
			return
		}
		s.finish(ErrUnreachable.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		msg := fmt.Sprintf("Ollama: HTTP %d", resp.StatusCode)
		if e.Error != "" {
			msg += ": " + clip(e.Error, 300)
		}
		s.finish(msg)
		return
	}
	layers := map[string][2]int64{} // digest -> total, completed
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	success := false
	for sc.Scan() {
		var line struct {
			Status    string `json:"status"`
			Digest    string `json:"digest"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			s.finish("Ollama: " + clip(line.Error, 300))
			return
		}
		if line.Digest != "" && line.Total > 0 {
			layers[line.Digest] = [2]int64{line.Total, line.Completed}
		}
		var total, done int64
		for _, l := range layers {
			total += l[0]
			done += l[1]
		}
		st := clip(line.Status, 120)
		s.update(func(p *PullStatus) {
			if st != "" {
				p.Status = st
			}
			p.Total, p.Completed = total, done
		})
		if line.Status == "success" {
			success = true
		}
	}
	switch {
	case ctx.Err() != nil:
		s.finish("download cancelled")
	case sc.Err() != nil:
		s.finish("the download was interrupted: try again (Ollama resumes it)")
	case !success:
		s.finish("the download ended without success: try again")
	default:
		s.finish("")
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
