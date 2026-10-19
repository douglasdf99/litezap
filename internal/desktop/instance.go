package desktop

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrAlreadyRunning means another LiteZap app owns the app folder; it was asked to show
// its dashboard.
var ErrAlreadyRunning = errors.New("o LiteZap já está aberto")

// Instance is the claim of the running app on its app folder: a local socket that a
// second launch dials to ask this one to show itself instead of starting a second engine
// (which would fail on the port and fight over the WhatsApp sessions).
type Instance struct {
	ln       net.Listener
	path     string
	show     chan struct{}
	once     sync.Once
	closeErr error
}

// maxSocketPath stays under the sun_path limit (104 bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// instanceSocket is <dir>/app.sock, or a per-folder name in the temp folder when that
// path is too long for a unix socket.
func instanceSocket(dir string) string {
	p := filepath.Join(dir, "app.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(os.TempDir(), "litezap-"+hex.EncodeToString(sum[:8])+".sock")
}

// ClaimInstance makes this process the LiteZap app of dir. When another one already runs,
// it asks it to show the dashboard and returns ErrAlreadyRunning.
func ClaimInstance(dir string) (*Instance, error) {
	path := instanceSocket(dir)
	if poke(path) {
		return nil, ErrAlreadyRunning
	}
	// Nobody answered: the socket, if any, is left over from a crash.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	in := &Instance{ln: ln, path: path, show: make(chan struct{}, 1)}
	go in.serve()
	return in, nil
}

// poke asks the instance listening on path to show itself.
func poke(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("show\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.TrimSpace(line) == "ok"
}

func (in *Instance) serve() {
	for {
		c, err := in.ln.Accept()
		if err != nil {
			return // closed
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "show" {
				return
			}
			select {
			case in.show <- struct{}{}:
			default: // one pending request is enough
			}
			_, _ = c.Write([]byte("ok\n"))
		}()
	}
}

// Show receives a value each time another launch asked this app to show itself.
func (in *Instance) Show() <-chan struct{} { return in.show }

// Close releases the claim.
func (in *Instance) Close() error {
	in.once.Do(func() {
		in.closeErr = in.ln.Close()
		_ = os.Remove(in.path)
	})
	return in.closeErr
}
