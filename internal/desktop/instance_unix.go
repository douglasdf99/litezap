//go:build !windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes an exclusive, non-blocking flock on f (false: another process holds it).
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// ownedPrivate checks that fi belongs to this user and has no group/other access.
func ownedPrivate(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: dono desconhecido", fi.Name())
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s pertence a outro usuário", fi.Name())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s tem permissão %v (precisa ser 0700)", fi.Name(), fi.Mode().Perm())
	}
	return nil
}
