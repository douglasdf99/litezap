package serve

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// The removed Cardápio's photo folder is deleted on start; the rest of the data stays.
func TestRemoveMenuData(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "menu", "inst1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "menu", "inst1", "a.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "media"), 0o700); err != nil {
		t.Fatal(err)
	}
	removeMenuData(dir, slog.New(slog.DiscardHandler))
	if _, err := os.Stat(filepath.Join(dir, "menu")); !os.IsNotExist(err) {
		t.Fatalf("data/menu still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media")); err != nil {
		t.Fatalf("data/media removed: %v", err)
	}
	removeMenuData(dir, slog.New(slog.DiscardHandler)) // already gone: nothing to do
}
