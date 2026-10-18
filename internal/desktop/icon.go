package desktop

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The LiteZap mark (chat bubble + bolt) in its 32x32 design grid, the same shapes as the
// site's logo.svg, rasterized here so the tray needs no image files or SVG renderer.
var boltPoly = [][2]float64{{17.6, 7.5}, {11, 17}, {15.6, 17}, {14.3, 24.5}, {21, 15}, {16.4, 15}}
var tailPoly = [][2]float64{{6.2, 20.5}, {4.5, 27.5}, {11.5, 25.2}}

var (
	violetA = color.NRGBA{0x7B, 0x61, 0xFF, 0xFF}
	violetB = color.NRGBA{0x4B, 0x2F, 0xD6, 0xFF}
	zap     = color.NRGBA{0xFF, 0xC5, 0x3D, 0xFF}
)

func inPoly(p [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		xi, yi, xj, yj := p[i][0], p[i][1], p[j][0], p[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

func inBubble(x, y float64) bool {
	dx, dy := (x-16)/12.5, (y-15.1)/11.6
	return dx*dx+dy*dy <= 1 || inPoly(tailPoly, x, y)
}

// IconImage draws the mark at size x size pixels with 4x4 supersampling.
func IconImage(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	scale := 32 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					var c color.NRGBA
					switch {
					case inPoly(boltPoly, x, y):
						c = zap
					case inBubble(x, y):
						// Linear gradient from (4,3) to (28,29), as in logo.svg.
						t := ((x-4)*24 + (y-3)*26) / (24*24 + 26*26)
						t = math.Max(0, math.Min(1, t))
						c = color.NRGBA{lerp(violetA.R, violetB.R, t), lerp(violetA.G, violetB.G, t), lerp(violetA.B, violetB.B, t), 0xFF}
					default:
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a++
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / (ss * ss))})
		}
	}
	return img
}

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5) }

// IconPNG returns the mark as a PNG.
func IconPNG(size int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, IconImage(size))
	return buf.Bytes()
}

// IconICO wraps a PNG of the mark in a single-image .ico (Windows tray icons must be ICO;
// PNG-in-ICO is supported since Windows Vista).
func IconICO(size int) []byte {
	p := IconPNG(size)
	var buf bytes.Buffer
	dim := byte(size)
	if size >= 256 {
		dim = 0 // 0 means 256 in an ICONDIRENTRY
	}
	_ = binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, 1}) // reserved, type icon, count
	buf.Write([]byte{dim, dim, 0, 0})                               // width, height, palette, reserved
	_ = binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})   // planes, bits per pixel
	_ = binary.Write(&buf, binary.LittleEndian, [2]uint32{uint32(len(p)), 6 + 16})
	buf.Write(p)
	return buf.Bytes()
}
