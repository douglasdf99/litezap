package spawner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Worker output (stdout and stderr) goes through the supervisor: each line gets the
// instance id and lands in the supervisor's log (the desktop app's litezap.log, or the
// stderr of `litezap serve`). Lines are capped and rate-limited per worker, so a flood of
// warnings cannot fill the disk; dropped lines are counted in a notice.
const (
	maxOutputLine  = 4096             // bytes kept of one line
	outputBurst    = 200              // lines a worker may write at once
	outputPerSec   = 20               // sustained lines per second
	outputDrainMax = 2 * time.Second  // how long Done waits for the last lines
	outputTickMin  = time.Millisecond // limiter granularity floor (tests)
)

// SyncWriter serializes writes of whole lines to w (shared by every worker and safe
// for concurrent use).
type SyncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewSyncWriter wraps w.
func NewSyncWriter(w io.Writer) *SyncWriter { return &SyncWriter{w: w} }

func (s *SyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// lineLimiter is a token bucket counted in lines.
type lineLimiter struct {
	burst, perSec float64
	tokens        float64
	last          time.Time
	now           func() time.Time
}

func newLineLimiter(burst, perSec int, now func() time.Time) *lineLimiter {
	if now == nil {
		now = time.Now
	}
	return &lineLimiter{burst: float64(burst), perSec: float64(perSec), tokens: float64(burst), last: now(), now: now}
}

func (l *lineLimiter) allow() bool {
	t := l.now()
	if d := t.Sub(l.last); d >= outputTickMin {
		l.tokens = min(l.burst, l.tokens+d.Seconds()*l.perSec)
		l.last = t
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// outputPipe copies the lines of one worker into out.
type outputPipe struct {
	id      string
	out     io.Writer
	lim     *lineLimiter
	maxLine int
	dropped int
}

func newOutputPipe(id string, out io.Writer, now func() time.Time) *outputPipe {
	return &outputPipe{id: id, out: out, lim: newLineLimiter(outputBurst, outputPerSec, now), maxLine: maxOutputLine}
}

// run reads r until EOF (the worker and its copies of the pipe are gone).
func (p *outputPipe) run(r io.Reader) {
	br := bufio.NewReaderSize(r, p.maxLine)
	for {
		line, cut, err := readCappedLine(br, p.maxLine)
		if len(line) > 0 || cut > 0 {
			p.emit(line, cut)
		}
		if err != nil {
			break
		}
	}
	if p.dropped > 0 {
		p.notice()
	}
}

// readCappedLine reads one line (without its end of line), keeping at most max bytes;
// cut is how many bytes were left out.
func readCappedLine(br *bufio.Reader, max int) (line []byte, cut int, err error) {
	for {
		frag, isPrefix, e := br.ReadLine()
		if room := max - len(line); room > 0 {
			n := min(room, len(frag))
			line = append(line, frag[:n]...)
			cut += len(frag) - n
		} else {
			cut += len(frag)
		}
		if e != nil {
			return line, cut, e
		}
		if !isPrefix {
			return line, cut, nil
		}
	}
}

func (p *outputPipe) emit(line []byte, cut int) {
	if !p.lim.allow() {
		p.dropped++
		return
	}
	if p.dropped > 0 {
		p.notice()
	}
	_, _ = p.out.Write(p.format(line, cut))
}

// notice reports the lines dropped since the last one (not rate-limited itself, but at
// most one per allowed line or per exit).
func (p *outputPipe) notice() {
	msg := fmt.Sprintf("%d lines of worker output dropped (rate limit)", p.dropped)
	p.dropped = 0
	_, _ = p.out.Write(p.format([]byte(msg), 0))
}

// format prefixes a line with the instance id. JSON lines (LITEZAP_LOG_FORMAT=json) stay
// JSON: the id becomes their first field, unless the worker's logger already set it.
func (p *outputPipe) format(line []byte, cut int) []byte {
	line = bytes.TrimRight(line, "\r")
	var b bytes.Buffer
	if cut == 0 && len(line) > 1 && line[0] == '{' && line[len(line)-1] == '}' {
		if bytes.Contains(line, []byte(`"instance_id":`)) {
			b.Write(line)
		} else {
			id, _ := json.Marshal(p.id)
			b.WriteString(`{"instance_id":`)
			b.Write(id)
			if len(bytes.TrimSpace(line[1:len(line)-1])) > 0 {
				b.WriteByte(',')
			}
			b.Write(line[1:])
		}
	} else {
		b.WriteString("worker[")
		b.WriteString(p.id)
		b.WriteString("] ")
		b.Write(line)
		if cut > 0 {
			fmt.Fprintf(&b, " …[truncated %d bytes]", cut)
		}
	}
	b.WriteByte('\n')
	return b.Bytes()
}
