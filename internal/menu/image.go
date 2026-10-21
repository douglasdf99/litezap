package menu

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif" // decoders
	"image/jpeg"
	_ "image/png"
)

// Image limits: an upload is at most MaxImageBytes and MaxImagePixels; it is stored as a
// JPEG of at most ImageSide pixels on its longest side. Peak memory is the decoded
// source (at most 10 MP: ~40 MB for an RGBA PNG, ~15 MB for a JPEG) plus the small
// destination: the source is scaled straight into the destination, never copied.
const (
	MaxImageBytes  = 8 << 20
	MaxImagePixels = 10_000_000
	ImageSide      = 1080
	imageQuality   = 85
)

// ErrImage is a file that is not a usable JPEG, PNG or GIF image.
var ErrImage = errors.New("a imagem deve ser JPEG, PNG ou GIF de até 8 MB e 10 megapixels")

// NormalizeImage decodes an image, scales it down to ImageSide (area averaging, so
// photos stay sharp) on a white background (transparent PNGs) and encodes a JPEG.
func NormalizeImage(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return nil, ErrImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return nil, ErrImage
	}
	if format != "jpeg" && format != "png" && format != "gif" {
		return nil, ErrImage
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrImage
	}
	b := src.Bounds()
	if int64(b.Dx())*int64(b.Dy()) > MaxImagePixels || b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, ErrImage
	}
	w, h := b.Dx(), b.Dy()
	if w > ImageSide || h > ImageSide {
		if w >= h {
			w, h = ImageSide, max(1, h*ImageSide/b.Dx())
		} else {
			w, h = max(1, w*ImageSide/b.Dy()), ImageSide
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scaleOnWhite(src, w, h), &jpeg.Options{Quality: imageQuality}); err != nil {
		return nil, ErrImage
	}
	return out.Bytes(), nil
}

// scaleOnWhite averages the source pixels covered by each destination pixel, composited
// on white (JPEG has no alpha), writing only the w×h destination.
func scaleOnWhite(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1 && sy < sh; sy++ {
				for sx := x0; sx < x1 && sx < sw; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA() // premultiplied, 16 bit
					white := uint64(0xffff - ca)
					r += uint64(cr) + white
					g += uint64(cg) + white
					bl += uint64(cb) + white
					n++
				}
			}
			if n == 0 {
				continue
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 255
		}
	}
	return dst
}
