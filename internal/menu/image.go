package menu

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders
	"image/jpeg"
	_ "image/png"
)

// Image limits: an upload is at most MaxImageBytes and MaxImagePixels (decoding a
// larger one would take too much memory); it is stored as a JPEG of at most
// ImageSide pixels on its longest side.
const (
	MaxImageBytes  = 8 << 20
	MaxImagePixels = 4096 * 4096
	ImageSide      = 1080
	imageQuality   = 85
)

// ErrImage is a file that is not a usable JPEG, PNG or GIF image.
var ErrImage = errors.New("a imagem deve ser JPEG, PNG ou GIF de até 8 MB e 16 megapixels")

// NormalizeImage decodes an image, scales it down to ImageSide (area averaging, so
// photos stay sharp) on a white background (transparent PNGs) and encodes a JPEG.
func NormalizeImage(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return nil, ErrImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxImagePixels {
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
	w, h := b.Dx(), b.Dy()
	if w > ImageSide || h > ImageSide {
		if w >= h {
			w, h = ImageSide, max(1, h*ImageSide/b.Dx())
		} else {
			w, h = max(1, w*ImageSide/b.Dy()), ImageSide
		}
	}
	// Flatten on white first (JPEG has no alpha).
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Over)
	dst := flat
	if w != b.Dx() || h != b.Dy() {
		dst = downscale(flat, w, h)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: imageQuality}); err != nil {
		return nil, ErrImage
	}
	return out.Bytes(), nil
}

// downscale averages the source pixels covered by each destination pixel.
func downscale(src *image.RGBA, w, h int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1 && sy < sh; sy++ {
				off := sy*src.Stride + x0*4
				for sx := x0; sx < x1 && sx < sw; sx++ {
					r += uint64(src.Pix[off])
					g += uint64(src.Pix[off+1])
					bl += uint64(src.Pix[off+2])
					off += 4
					n++
				}
			}
			if n == 0 {
				continue
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}
