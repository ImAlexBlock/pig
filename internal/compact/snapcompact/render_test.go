package snapcompact

import (
	"bytes"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestRenderASCIIFrame(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line of ascii text 0123456789\n")
	}
	frames, err := Render(sb.String(), Options{FrameWidth: 1568, MaxFrameHeight: 2048})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	img, err := png.Decode(bytes.NewReader(frames[0].PNG))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	if got := img.Bounds().Dx(); got != 1568 {
		t.Fatalf("frame width = %d, want 1568", got)
	}
	dark := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r < 0x4000 && g < 0x4000 && bl < 0x4000 {
				dark++
			}
		}
	}
	if dark == 0 {
		t.Fatal("frame has no dark pixels")
	}
}

func TestCJKWidth(t *testing.T) {
	if !IsWide('中') {
		t.Fatal("CJK rune not reported as wide")
	}
	if Advance('中') != 2 {
		t.Fatalf("CJK advance = %d, want 2 half-columns", Advance('中'))
	}
	if IsWide('a') || Advance('a') != 1 {
		t.Fatal("ASCII rune must advance one half-column")
	}
	if !IsWide('あ') || !IsWide('ア') || !IsWide('。') {
		t.Fatal("kana and CJK punctuation must be wide")
	}
}

func TestGlyphsLoaded(t *testing.T) {
	if !HasGlyph('A') {
		t.Fatal("glyph table lacks ASCII")
	}
	if !HasGlyph('中') {
		t.Skip("CJK glyphs are not baked into this build (Go Mono fallback)")
	}
	if Glyph('中') == nil {
		t.Fatal("CJK glyph bitmap missing")
	}
}

func TestMixedFixtureRenders(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/snapcompact/mixed.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	frames, err := Render(string(data), Options{FrameWidth: 1568, MaxFrameHeight: 2048})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	for i, f := range frames {
		if _, err := png.Decode(bytes.NewReader(f.PNG)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
}

func TestNormalizeStripsANSI(t *testing.T) {
	got := Normalize("\x1b[31mred\x1b[0m\tend\r\n")
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Fatalf("normalize left control bytes: %q", got)
	}
	if !strings.Contains(got, "    ") {
		t.Fatalf("tab was not expanded: %q", got)
	}
}
