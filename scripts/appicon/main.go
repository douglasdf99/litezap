// Command appicon writes the LiteZap mark as a macOS .iconset folder (turn it into an
// .icns with `iconutil -c icns`). Used by cmd/litezap-app/package-macos.sh.
//
//	go run ./scripts/appicon <out.iconset>
package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/douglasdf99/litezap/internal/desktop"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: appicon <out.iconset>")
		os.Exit(2)
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail(err)
	}
	for _, base := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			name := fmt.Sprintf("icon_%dx%d.png", base, base)
			if scale == 2 {
				name = fmt.Sprintf("icon_%dx%d@2x.png", base, base)
			}
			if err := writePNG(filepath.Join(out, name), padded(base*scale)); err != nil {
				fail(err)
			}
		}
	}
}

// padded draws the mark at ~82% of size, centered, like the margin of macOS app icons.
func padded(size int) image.Image {
	inner := size * 82 / 100
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	off := (size - inner) / 2
	draw.Draw(img, image.Rect(off, off, off+inner, off+inner), desktop.IconImage(inner), image.Point{}, draw.Src)
	return img
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "appicon:", err)
	os.Exit(1)
}
