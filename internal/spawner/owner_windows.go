//go:build windows

package spawner

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return true }
