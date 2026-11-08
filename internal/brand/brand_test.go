package brand

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// A logo: white background, a red block and a smaller navy block.
func logo() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{255, 255, 255, 255}
			switch {
			case x >= 20 && x < 120 && y >= 20 && y < 80:
				c = color.RGBA{220, 30, 40, 255}
			case x >= 130 && x < 180 && y >= 20 && y < 80:
				c = color.RGBA{20, 30, 90, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func near(hex string, r, g, b int) bool {
	var hr, hg, hb int
	if _, err := fmtSscan(hex, &hr, &hg, &hb); err != nil {
		return false
	}
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	return abs(hr-r) < 12 && abs(hg-g) < 12 && abs(hb-b) < 12
}

func TestImagePaletteDropsBackground(t *testing.T) {
	p, err := ImagePalette(logo())
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 2 || !near(p[0].Hex, 220, 30, 40) || !near(p[1].Hex, 20, 30, 90) {
		t.Fatalf("palette = %+v", p)
	}
	if p[0].Share < 0.6 || p[0].Share > 0.75 {
		t.Errorf("red share = %v", p[0].Share)
	}
}

func TestTransparentBackground(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	for y := 10; y < 40; y++ {
		for x := 10; x < 40; x++ {
			img.Set(x, y, color.NRGBA{0, 160, 120, 255})
		}
	}
	p := Extract(img)
	if len(p) != 1 || !near(p[0].Hex, 0, 160, 120) || p[0].Share != 1 {
		t.Fatalf("palette = %+v", p)
	}
}

func TestMerge(t *testing.T) {
	m := Merge([]Swatch{{"#dc1e28", 0.7}, {"#141e5a", 0.3}}, []Swatch{{"#de1f2a", 1}}, nil)
	if len(m) != 2 || !near(m[0].Hex, 220, 30, 40) || m[0].Share != 0.85 {
		t.Fatalf("merge = %+v", m)
	}
}

func TestKitSanitize(t *testing.T) {
	k := Kit{Logo: "tengo", Colors: "otro", Data: "cero", Palette: []string{"#ABCDEF", "red", "#abcdef", "#000000"}, TeamSize: 3, AccessUsers: 5}.Sanitize()
	if k.Logo != "tengo" || k.Colors != "" || k.Data != "cero" || len(k.Palette) != 2 || k.Palette[0] != "#abcdef" || k.TeamSize != 5 {
		t.Fatalf("kit = %+v", k)
	}
	if CompanySize(1) != "solo" || CompanySize(8) != "1-10" || CompanySize(300) != "200+" {
		t.Error("company size")
	}
}

func fmtSscan(hex string, r, g, b *int) (int, error) {
	return fmt.Sscanf(hex, "#%02x%02x%02x", r, g, b)
}

func TestImagePaletteRejectsHugeImages(t *testing.T) {
	// A valid PNG header declaring 20000x20000 pixels: refused before decoding.
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	b := buf.Bytes()
	b[16], b[17], b[18], b[19] = 0, 0, 0x4e, 0x20 // width 20000
	b[20], b[21], b[22], b[23] = 0, 0, 0x4e, 0x20 // height 20000
	if _, err := ImagePalette(b); err == nil {
		t.Fatal("huge image accepted")
	}
}
