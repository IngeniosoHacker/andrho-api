package brand

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // decoders
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"

	_ "golang.org/x/image/webp"
)

// Swatch is one color of a palette and how much of the image it covers
// (0..1, background left out).
type Swatch struct {
	Hex   string  `json:"hex"`
	Share float64 `json:"share"`
}

// MaxPixels is the largest image read (a 30 MP photo).
const MaxPixels = 30_000_000

// Palette limits.
const (
	maxSide    = 160 // pixels sampled per side
	minShare   = 0.02
	mergeDist  = 38.0 // RGB distance under which two colors are the same
	maxSwatchs = 6
)

// ImagePalette decodes an image (PNG, JPEG, WebP or GIF) and reads its
// main colors. The background (a color covering most of the border, or
// transparency) is left out, so a logo on white gives the logo's colors.
func ImagePalette(data []byte) ([]Swatch, error) {
	// A few KB can declare an enormous image: check before decoding.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("brand: imagen ilegible: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return nil, fmt.Errorf("brand: imagen demasiado grande (%dx%d)", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("brand: imagen ilegible: %w", err)
	}
	return Extract(img), nil
}

type bucket struct {
	r, g, b float64 // sums
	n       float64
}

func (b bucket) rgb() [3]float64 { return [3]float64{b.r / b.n, b.g / b.n, b.b / b.n} }

func dist(a, b [3]float64) float64 {
	// Weighted ("redmean") distance: closer to how colors look.
	rm := (a[0] + b[0]) / 2
	dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return math.Sqrt((2+rm/256)*dr*dr+4*dg*dg+(2+(255-rm)/256)*db*db) / 3
}

// Extract reads the palette of an image: pixels are sampled on a grid,
// quantized to 4 bits per channel, the buckets averaged, then close
// colors merged and the tiny ones dropped.
func Extract(img image.Image) []Swatch {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return []Swatch{}
	}
	step := max(1, max(w, h)/maxSide)
	buckets := map[int]*bucket{}
	border := map[int]float64{}
	var borderN float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue // transparent: background
			}
			// Un-premultiply, 8 bits.
			fr, fg, fb := float64(r)*255/float64(a), float64(g)*255/float64(a), float64(b)*255/float64(a)
			key := int(fr)>>4<<8 | int(fg)>>4<<4 | int(fb)>>4
			bk := buckets[key]
			if bk == nil {
				bk = &bucket{}
				buckets[key] = bk
			}
			bk.r, bk.g, bk.b, bk.n = bk.r+fr, bk.g+fg, bk.b+fb, bk.n+1
			if x < bounds.Min.X+step || y < bounds.Min.Y+step || x+step >= bounds.Max.X || y+step >= bounds.Max.Y {
				border[key]++
				borderN++
			}
		}
	}
	// The background: one color on most of the border.
	bg := -1
	for k, n := range border {
		if n/borderN >= 0.6 {
			bg = k
		}
	}
	var bgColor [3]float64
	if bg >= 0 {
		bgColor = buckets[bg].rgb()
	}

	type col struct {
		c [3]float64
		n float64
	}
	var cols []col
	var total float64
	for k, b := range buckets {
		c := b.rgb()
		if bg >= 0 && (k == bg || dist(c, bgColor) < mergeDist/2) {
			continue
		}
		cols = append(cols, col{c, b.n})
		total += b.n
	}
	if total == 0 {
		// Only background (a flat image): that is the color.
		if bg >= 0 {
			return []Swatch{{Hex: hex(bgColor), Share: 1}}
		}
		return []Swatch{}
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].n > cols[j].n })
	var merged []col
	for _, c := range cols {
		joined := false
		for i := range merged {
			if dist(merged[i].c, c.c) < mergeDist {
				m := &merged[i]
				for j := 0; j < 3; j++ {
					m.c[j] = (m.c[j]*m.n + c.c[j]*c.n) / (m.n + c.n)
				}
				m.n += c.n
				joined = true
				break
			}
		}
		if !joined {
			merged = append(merged, c)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].n > merged[j].n })
	out := []Swatch{}
	for _, m := range merged {
		s := m.n / total
		if s < minShare || len(out) == maxSwatchs {
			break
		}
		out = append(out, Swatch{Hex: hex(m.c), Share: math.Round(s*1000) / 1000})
	}
	return out
}

func hex(c [3]float64) string {
	clamp := func(v float64) int { return int(math.Max(0, math.Min(255, math.Round(v)))) }
	return fmt.Sprintf("#%02x%02x%02x", clamp(c[0]), clamp(c[1]), clamp(c[2]))
}

// Merge combines the palettes of several images, each weighing the same.
func Merge(palettes ...[]Swatch) []Swatch {
	type col struct {
		c [3]float64
		n float64
	}
	var all []col
	n := 0
	for _, p := range palettes {
		if len(p) == 0 {
			continue
		}
		n++
		for _, s := range p {
			var r, g, b int
			if _, err := fmt.Sscanf(s.Hex, "#%02x%02x%02x", &r, &g, &b); err == nil {
				all = append(all, col{[3]float64{float64(r), float64(g), float64(b)}, s.Share})
			}
		}
	}
	if n == 0 {
		return []Swatch{}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].n > all[j].n })
	var merged []col
	for _, c := range all {
		joined := false
		for i := range merged {
			if dist(merged[i].c, c.c) < mergeDist {
				merged[i].n += c.n
				joined = true
				break
			}
		}
		if !joined {
			merged = append(merged, c)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].n > merged[j].n })
	out := []Swatch{}
	for _, m := range merged {
		if len(out) == MaxPalette {
			break
		}
		out = append(out, Swatch{Hex: hex(m.c), Share: math.Round(m.n/float64(n)*1000) / 1000})
	}
	return out
}

// Hexes is a palette's colors.
func Hexes(p []Swatch) []string {
	out := make([]string, 0, len(p))
	for _, s := range p {
		out = append(out, s.Hex)
	}
	return out
}
