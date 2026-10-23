package desktop

import (
	"os"
	"sync"
)

// LogFile is the app's log file (litezap.log). Past max bytes it starts over, keeping
// one old copy (<path>.1), when opened and while running: worker output now lands here
// too, and the app can run for weeks. Safe for concurrent use.
type LogFile struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenLog opens (appending) the log at path.
func OpenLog(path string, max int64) (*LogFile, error) {
	l := &LogFile{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *LogFile) open() error {
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > l.max {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.f, l.size = f, 0
	if fi, err := f.Stat(); err == nil {
		l.size = fi.Size()
	}
	return nil
}

func (l *LogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		l.rotate()
		if l.f == nil {
			return 0, os.ErrClosed
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate moves the full log to <path>.1 and starts a new one (l.f is nil when the new
// file cannot be created).
func (l *LogFile) rotate() {
	_ = l.f.Close()
	l.f = nil
	_ = os.Rename(l.path, l.path+".1")
	if f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		l.f, l.size = f, 0
	}
}

// Close closes the file.
func (l *LogFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
