package spawner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// SocketPath returns a short unix socket path (macOS limits paths to ~104 bytes):
// <tmp>/lz-<hash(dataDir)>/<hash(instanceID)>.sock
func SocketPath(dataDir, instanceID string) string {
	return filepath.Join(os.TempDir(), "lz-"+short(dataDir), short(instanceID)+".sock")
}

func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// ensureSocketDir creates the directory that holds sock and verifies it is safe to use:
// a real directory (not a symlink) owned by the current user with mode 0700.
func ensureSocketDir(sock string) error {
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("socket dir %s is not a plain directory", dir)
	}
	if runtime.GOOS != "windows" {
		if !ownedByCurrentUser(fi) {
			return fmt.Errorf("socket dir %s is not owned by the current user", dir)
		}
		if fi.Mode().Perm() != 0o700 {
			if err := os.Chmod(dir, 0o700); err != nil {
				return fmt.Errorf("socket dir %s: %w", dir, err)
			}
		}
	}
	return nil
}
