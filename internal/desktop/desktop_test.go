package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/png"
	"path/filepath"
	"testing"
)

func TestAppDir(t *testing.T) {
	home := func() (string, error) { return "/home/ana", nil }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"darwin", nil, filepath.Join("/home/ana", "Library", "Application Support", "LiteZap")},
		{"windows", map[string]string{"APPDATA": `C:\Users\ana\AppData\Roaming`}, filepath.Join(`C:\Users\ana\AppData\Roaming`, "LiteZap")},
		{"windows", nil, filepath.Join("/home/ana", "AppData", "Roaming", "LiteZap")},
		{"linux", nil, filepath.Join("/home/ana", ".local", "share", "litezap")},
		{"linux", map[string]string{"XDG_DATA_HOME": "/data/xdg"}, filepath.Join("/data/xdg", "litezap")},
		{"linux", map[string]string{"XDG_DATA_HOME": "relative/xdg"}, filepath.Join("/home/ana", ".local", "share", "litezap")},
		{"freebsd", nil, filepath.Join("/home/ana", ".local", "share", "litezap")},
		{"darwin", map[string]string{"LITEZAP_APP_DIR": "/tmp/lz-test"}, "/tmp/lz-test"},
	}
	for _, c := range cases {
		got, err := appDir(c.goos, env(c.env), home)
		if err != nil || got != c.want {
			t.Errorf("%s %v: got %q, %v; want %q", c.goos, c.env, got, err, c.want)
		}
	}
	if _, err := appDir("linux", env(nil), func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Error("missing home accepted")
	}
	if _, err := appDir("linux", env(map[string]string{"LITEZAP_APP_DIR": "rel"}), home); err == nil {
		t.Error("relative LITEZAP_APP_DIR accepted")
	}
}

func TestOpenCommand(t *testing.T) {
	url := "http://127.0.0.1:8787/app/#code=abc&x=1"
	for goos, want := range map[string][]string{
		"darwin":  {"open", url},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", url},
		"linux":   {"xdg-open", url},
	} {
		cmd := openCommand(goos, url)
		args := append([]string{filepath.Base(cmd.Path)}, cmd.Args[1:]...)
		if len(args) != len(want) {
			t.Fatalf("%s: args %v", goos, cmd.Args)
		}
		for i := range want {
			if filepath.Base(args[i]) != want[i] && args[i] != want[i] {
				t.Fatalf("%s: args %v, want %v", goos, cmd.Args, want)
			}
		}
	}
}

func TestIcon(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(IconPNG(64)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("size %v", b)
	}
	if _, _, _, a := img.At(1, 1).RGBA(); a != 0 {
		t.Error("corner should be transparent")
	}
	// Left part of the bubble is violet; the bolt's middle is yellow.
	if r, g, b, a := img.At(14, 30).RGBA(); a == 0 || b <= g || r>>8 > 0x90 {
		t.Errorf("bubble pixel not violet: %x %x %x %x", r>>8, g>>8, b>>8, a>>8)
	}
	if r, g, b, _ := img.At(32, 32).RGBA(); r>>8 < 0xF0 || g>>8 < 0xB0 || b>>8 > 0x80 {
		t.Errorf("bolt pixel not yellow: %x %x %x", r>>8, g>>8, b>>8)
	}

	ico := IconICO(64)
	var hdr [3]uint16
	_ = binary.Read(bytes.NewReader(ico[:6]), binary.LittleEndian, &hdr)
	if hdr != [3]uint16{0, 1, 1} || ico[6] != 64 || ico[7] != 64 {
		t.Fatalf("ico header %v %d %d", hdr, ico[6], ico[7])
	}
	size := binary.LittleEndian.Uint32(ico[14:18])
	off := binary.LittleEndian.Uint32(ico[18:22])
	if off != 22 || int(off+size) != len(ico) {
		t.Fatalf("ico entry size=%d offset=%d len=%d", size, off, len(ico))
	}
	if _, err := png.Decode(bytes.NewReader(ico[off:])); err != nil {
		t.Fatalf("ico payload: %v", err)
	}
	if IconICO(256)[6] != 0 {
		t.Error("256px icon must encode its size as 0")
	}
}
