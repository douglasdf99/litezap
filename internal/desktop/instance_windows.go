package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive, non-blocking lock on f (false: another process holds it).
func tryLock(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// ownedPrivate: the candidates live under %APPDATA% / %LOCALAPPDATA%, which Windows
// already restricts to the user through ACLs (there are no POSIX modes to check).
func ownedPrivate(os.FileInfo) error { return nil }
