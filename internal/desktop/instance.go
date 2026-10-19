package desktop

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// Instance is the claim of the running app on its app folder: an exclusive lock on
// <socket>.lock, held while the app runs, and a local socket that a second launch dials
// to ask this one to show itself instead of starting a second engine (which would fail
// on the port and fight over the WhatsApp sessions).
type Instance struct {
	ln   *net.UnixListener
	path string
	fi   os.FileInfo // the socket this instance created (Close removes only that one)
	lock *os.File
	show chan struct{}
	once sync.Once
}

// maxSocketPath stays under the sun_path limit (104 bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// socketCandidates lists where the socket may go, in order: the app folder, then
// $XDG_RUNTIME_DIR, then the user cache folder. Never a shared folder such as /tmp.
func socketCandidates(dir string, getenv func(string) string, cacheDir func() (string, error)) []string {
	sum := sha256.Sum256([]byte(dir))
	name := "litezap-" + hex.EncodeToString(sum[:8]) + ".sock"
	out := []string{filepath.Join(dir, "app.sock")}
	if d := getenv("XDG_RUNTIME_DIR"); d != "" && filepath.IsAbs(d) {
		out = append(out, filepath.Join(d, name))
	}
	if d, err := cacheDir(); err == nil && filepath.IsAbs(d) {
		out = append(out, filepath.Join(d, "litezap", name))
	}
	return out
}

// instanceSocket picks the first candidate that fits a unix socket path and whose folder
// belongs to this user with no access for others (0700), creating the cache subfolder.
func instanceSocket(dir string) (string, error) {
	for _, p := range socketCandidates(dir, os.Getenv, os.UserCacheDir) {
		if len(p) > maxSocketPath {
			continue
		}
		parent := filepath.Dir(p)
		if filepath.Base(parent) == "litezap" {
			_ = os.MkdirAll(parent, 0o700)
		}
		if privateDir(parent) == nil {
			return p, nil
		}
	}
	return "", errors.New("nenhuma pasta privada (0700, do usuário) com caminho curto para o socket de instância única")
}

// privateDir checks that p is a folder owned by this user with no group/other access.
func privateDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s não é uma pasta", p)
	}
	return ownedPrivate(fi)
}

// ClaimInstance makes this process the LiteZap app of dir. When another one already runs,
// it asks it to show the dashboard and returns ErrAlreadyRunning.
func ClaimInstance(dir string) (*Instance, error) {
	path, err := instanceSocket(dir)
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLock(lock)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if !locked {
		lock.Close()
		// The owner may still be starting: give it a moment to listen.
		for i := 0; i < 10; i++ {
			if poke(path) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		return nil, ErrAlreadyRunning
	}
	// We hold the lock: a socket there is left over from a crash.
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	ln.SetUnlinkOnClose(false) // Close removes the file itself, only if it is still ours
	_ = os.Chmod(path, 0o600)
	fi, err := os.Lstat(path)
	if err != nil {
		ln.Close()
		lock.Close()
		return nil, err
	}
	in := &Instance{ln: ln, path: path, fi: fi, lock: lock, show: make(chan struct{}, 1)}
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
			line, err := bufio.NewReader(io.LimitReader(c, 64)).ReadString('\n')
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

// Close removes the socket (only if it is still the one this instance created) and
// releases the lock.
func (in *Instance) Close() error {
	var err error
	in.once.Do(func() {
		err = in.ln.Close()
		if fi, e := os.Lstat(in.path); e == nil && os.SameFile(fi, in.fi) {
			_ = os.Remove(in.path)
		}
		in.lock.Close() // closing the file drops the lock
	})
	return err
}
