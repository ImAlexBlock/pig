// Package snapcompact archives context as glyph-rendered PNG frames.
package snapcompact

import (
	_ "embed"
	"encoding/binary"
	"unicode"
)

//go:embed glyphs.bin
var glyphData []byte

// Cell geometry of the baked glyph table.
const (
	CellW = 16
	CellH = 16
)

// GlyphTable is the parsed glyph bitmap table.
type GlyphTable struct {
	CellW int
	CellH int
	index map[rune]uint32
	data  []byte
}

var table = loadTable()

func loadTable() *GlyphTable {
	t := &GlyphTable{CellW: CellW, CellH: CellH, index: map[rune]uint32{}}
	if len(glyphData) < 24 || string(glyphData[:8]) != "PIGFONT1" {
		return t
	}
	w := binary.LittleEndian.Uint32(glyphData[8:])
	h := binary.LittleEndian.Uint32(glyphData[12:])
	count := binary.LittleEndian.Uint32(glyphData[16:])
	indexOffset := int(binary.LittleEndian.Uint32(glyphData[20:]))
	end := indexOffset + int(count)*8
	if indexOffset < 24 || end > len(glyphData) {
		return t
	}
	t.CellW = int(w)
	t.CellH = int(h)
	t.data = glyphData[end:]
	for i := range int(count) {
		base := indexOffset + i*8
		cp := binary.LittleEndian.Uint32(glyphData[base:])
		off := binary.LittleEndian.Uint32(glyphData[base+4:])
		t.index[rune(cp)] = off
	}
	return t
}

// Glyph returns the bitmap for r, falling back to '?'.
func Glyph(r rune) []byte {
	if bits, ok := table.bitsFor(r); ok {
		return bits
	}
	if bits, ok := table.bitsFor('?'); ok {
		return bits
	}
	return nil
}

// HasGlyph reports whether the table contains r.
func HasGlyph(r rune) bool {
	_, ok := table.index[r]
	return ok
}

func (t *GlyphTable) bitsFor(r rune) ([]byte, bool) {
	off, ok := t.index[r]
	if !ok || t.data == nil {
		return nil, false
	}
	size := t.CellH * t.CellW / 8
	if int(off)+size > len(t.data) {
		return nil, false
	}
	return t.data[off : int(off)+size], true
}

// IsWide reports whether r advances two half-columns.
func IsWide(r rune) bool {
	if r < 0x1100 {
		return false
	}
	if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
		return true
	}
	switch {
	case r >= 0x3000 && r <= 0x303F,
		r >= 0xFF00 && r <= 0xFF60:
		return true
	}
	return false
}

// Advance returns the half-column advance for r.
func Advance(r rune) int {
	if IsWide(r) {
		return 2
	}
	return 1
}
