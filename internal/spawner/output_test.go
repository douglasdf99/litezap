package spawner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func pipeLines(t *testing.T, p *outputPipe, in string) []string {
	t.Helper()
	p.run(strings.NewReader(in))
	b := p.out.(*bytes.Buffer)
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

func TestOutputPipePrefixesTextAndJSONLines(t *testing.T) {
	var b bytes.Buffer
	p := newOutputPipe("inst_a", &b, nil)
	lines := pipeLines(t, p, "level=WARN msg=\"failed to decrypt\"\r\n"+
		`{"level":"WARN","msg":"x"}`+"\n"+
		`{"instance_id":"inst_a","msg":"y"}`+"\n"+
		"{}\n"+
		"panic: boom") // no final newline
	want := []string{
		`worker[inst_a] level=WARN msg="failed to decrypt"`,
		`{"instance_id":"inst_a","level":"WARN","msg":"x"}`,
		`{"instance_id":"inst_a","msg":"y"}`,
		`{"instance_id":"inst_a"}`,
		`worker[inst_a] panic: boom`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range lines[1:4] {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q is no longer JSON: %v", l, err)
		}
	}
}

func TestOutputPipeTruncatesLongLines(t *testing.T) {
	var b bytes.Buffer
	p := newOutputPipe("inst_a", &b, nil)
	p.maxLine = 10
	long := strings.Repeat("a", 5000)
	lines := pipeLines(t, p, long+"\nshort\n{"+strings.Repeat(`"k":1,`, 50)+`"z":1}`+"\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[0] != "worker[inst_a] aaaaaaaaaa …[truncated 4990 bytes]" {
		t.Fatalf("long line = %q", lines[0])
	}
	if lines[1] != "worker[inst_a] short" {
		t.Fatalf("next line = %q", lines[1])
	}
	// A cut JSON line is no longer JSON: it is shown as text.
	if !strings.HasPrefix(lines[2], "worker[inst_a] {") || !strings.Contains(lines[2], "truncated") {
		t.Fatalf("cut json = %q", lines[2])
	}
}

func TestOutputPipeRateLimit(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	var b bytes.Buffer
	p := newOutputPipe("inst_a", &b, clk.now)
	for i := 0; i < outputBurst+50; i++ {
		p.emit([]byte(fmt.Sprintf("line %d", i)), 0)
	}
	if got := strings.Count(b.String(), "\n"); got != outputBurst {
		t.Fatalf("burst wrote %d lines, want %d", got, outputBurst)
	}
	if p.dropped != 50 {
		t.Fatalf("dropped = %d", p.dropped)
	}
	// One second later the bucket has refilled outputPerSec lines; the first allowed
	// line is preceded by the notice of what was dropped.
	clk.t = clk.t.Add(time.Second)
	b.Reset()
	for i := 0; i < outputPerSec+5; i++ {
		p.emit([]byte("later"), 0)
	}
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if lines[0] != "worker[inst_a] 50 lines of worker output dropped (rate limit)" {
		t.Fatalf("first line = %q", lines[0])
	}
	if len(lines) != outputPerSec+1 || p.dropped != 5 {
		t.Fatalf("lines = %d dropped = %d", len(lines), p.dropped)
	}
	// Lines still dropped at exit are reported too.
	b.Reset()
	p.run(strings.NewReader(""))
	if !strings.Contains(b.String(), "5 lines of worker output dropped") {
		t.Fatalf("exit notice = %q", b.String())
	}
}

// syncBuffer is a bytes.Buffer safe for the pipe goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestProcessRoutesWorkerOutput(t *testing.T) {
	var out syncBuffer
	p := testSpawner(t, "chatty")
	p.Output = &out
	// The worker prints to stdout and stderr and exits: Start fails, but its lines must
	// already be in the supervisor's log.
	if _, err := p.Start(context.Background(), "inst_out"); err == nil {
		t.Fatal("expected start error")
	}
	got := out.String()
	for _, want := range []string{"worker[inst_out] to stdout\n", "worker[inst_out] to stderr\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q lacks %q", got, want)
		}
	}
}
