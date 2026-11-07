// Command genfont rasterises a font into the snapcompact glyph table.
//
// Source font: Noto Sans Mono CJK SC (SIL Open Font License 1.1), 16px grid.
// Usage:
//
//	go run ./cmd/genfont --font /path/NotoSansMonoCJKsc-Regular.otf \
//	    --out internal/compact/snapcompact/glyphs.bin
//
// Without --font the tool rasterises the bundled Go Mono face, which covers
// ASCII only.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"os"
	"sort"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	cellW         = 16
	cellH         = 16
	headerSize    = 24
	indexEntryLen = 8
)

func main() {
	fontPath := flag.String("font", "", "path to a TTF/OTF font (empty = bundled Go Mono)")
	out := flag.String("out", "internal/compact/snapcompact/glyphs.bin", "output glyph table")
	size := flag.Float64("size", 10.5, "font size in points")
	flag.Parse()

	data := gomono.TTF
	if *fontPath != "" {
		b, err := os.ReadFile(*fontPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genfont: read %s: %v\n", *fontPath, err)
			os.Exit(1)
		}
		data = b
	}
	f, err := opentype.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genfont: parse font: %v\n", err)
		os.Exit(1)
	}
	// Rasterise at 4x and box-downsample so the 1bpp cells keep their shape.
	const supersample = 4
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size: *size * supersample, DPI: 72, Hinting: font.HintingNone,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "genfont: face: %v\n", err)
		os.Exit(1)
	}
	defer face.Close()
	metrics := face.Metrics()
	ascent := metrics.Ascent.Ceil() / supersample
	descent := metrics.Descent.Ceil() / supersample
	// Centre the glyph box vertically inside the cell.
	baseline := ascent + (cellH-ascent-descent)/2
	if baseline < ascent {
		baseline = ascent
	}
	if max := cellH - descent; baseline > max {
		baseline = max
	}

	runes := glyphSet(f)
	big := image.NewAlpha(image.Rect(0, 0, cellW*supersample, cellH*supersample))
	mask := image.NewUniform(image.White)
	var bits []byte
	index := make([]glyphEntry, 0, len(runes))
	for _, r := range runes {
		draw.Draw(big, big.Bounds(), image.Transparent, image.Point{}, draw.Src)
		d := &font.Drawer{Dst: big, Src: mask, Face: face, Dot: fixed.P(0, baseline*supersample)}
		d.DrawString(string(r))
		index = append(index, glyphEntry{codepoint: uint32(r), offset: uint32(len(bits))})
		bits = append(bits, downsample(big, supersample)...)
	}
	sort.Slice(index, func(i, j int) bool { return index[i].codepoint < index[j].codepoint })

	buf := make([]byte, 0, headerSize+len(index)*indexEntryLen+len(bits))
	buf = append(buf, []byte("PIGFONT1")...)
	for _, v := range []uint32{cellW, cellH, uint32(len(index)), headerSize} {
		buf = binary.LittleEndian.AppendUint32(buf, v)
	}
	for _, e := range index {
		buf = binary.LittleEndian.AppendUint32(buf, e.codepoint)
		buf = binary.LittleEndian.AppendUint32(buf, e.offset)
	}
	buf = append(buf, bits...)
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genfont: write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("genfont: %d glyphs, %d bytes\n", len(index), len(buf))
}

type glyphEntry struct {
	codepoint uint32
	offset    uint32
}

// downsample box-averages the supersampled glyph into a 1bpp cell.
func downsample(src *image.Alpha, scale int) []byte {
	out := make([]byte, cellH*cellW/8)
	area := scale * scale
	for y := range cellH {
		for x := range cellW {
			sum := 0
			for dy := range scale {
				for dx := range scale {
					sum += int(src.AlphaAt(x*scale+dx, y*scale+dy).A)
				}
			}
			if sum/area < 90 {
				continue
			}
			out[y*(cellW/8)+x/8] |= 1 << (7 - uint(x%8))
		}
	}
	return out
}

// glyphSet returns the code points the font can render.
func glyphSet(f *sfnt.Font) []rune {
	var b sfnt.Buffer
	has := func(r rune) bool {
		gi, err := f.GlyphIndex(&b, r)
		return err == nil && gi != 0
	}
	ranges := [][2]rune{
		{0x20, 0x7E},
		{0xA0, 0xFF},
		{0x2000, 0x206F},
		{0x3000, 0x303F},
		{0x3040, 0x309F},
		{0x30A0, 0x30FF},
		{0x4E00, 0x9FFF},
		{0xFF00, 0xFFEF},
	}
	var out []rune
	for _, rg := range ranges {
		for r := rg[0]; r <= rg[1]; r++ {
			if has(r) {
				out = append(out, r)
			}
		}
	}
	return out
}
