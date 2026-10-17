package spawner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// SocketPath returns a short unix socket path (macOS limits paths to ~104 bytes):
// <tmp>/lz-<hash(dataDir)>/<hash(instanceID)>.sock
func SocketPath(dataDir, instanceID string) string {
	return filepath.Join(os.TempDir(), "lz-"+short(dataDir), short(instanceID)+".sock")
}

func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
