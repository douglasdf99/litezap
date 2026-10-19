//go:build !windows

package desktop

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAlertCommand(t *testing.T) {
	none := func(string) (string, error) { return "", errors.New("not found") }
	c := alertCommand("darwin", "LiteZap", `Diga "oi"`, none)
	if c == nil || filepath.Base(c.Path) != "osascript" || c.Args[len(c.Args)-1] != `Diga "oi"` {
		t.Errorf("darwin: %v", c)
	}
	if c := alertCommand("linux", "t", "m", none); c != nil {
		t.Errorf("linux without tools: %v", c.Args)
	}
	only := func(name string) func(string) (string, error) {
		return func(n string) (string, error) {
			if n == name {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
	}
	if c := alertCommand("linux", "t", "m", only("notify-send")); c == nil || c.Args[0] != "notify-send" {
		t.Errorf("linux notify-send: %v", c)
	}
}
