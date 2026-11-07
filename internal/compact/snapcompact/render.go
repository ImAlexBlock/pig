package snapcompact

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"

	"github.com/alexblock/pig/internal/rtk"
)

// Layout constants.
const (
	HalfColumn = 8
	Padding    = 32
	MaxRows    = 1 << 16
)

// Options configures rendering.
type Options struct {
	FrameWidth     int
	MaxFrameHeight int
}

// Frame is one rendered PNG page.
type Frame struct {
	PNG    []byte
	Width  int
	Height int
	Rows   int
}

// GlyphWidthHalf returns the advance of r in half-columns.
func GlyphWidthHalf(r rune) int {
	return Advance(r)
}

// Normalize strips escape sequences, expands tabs and folds CR.
func Normalize(text string) string {
	text = rtk.StripANSI(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\t", "    ")
	return text
}

type cell struct {
	r    rune
	half int // starting half-column
}

// layout wraps text into display lines of at most cols half-columns.
func layout(text string, cols int) [][]cell {
	if cols <= 0 {
		cols = 196
	}
	var lines [][]cell
	for _, raw := range strings.Split(text, "\n") {
		var line []cell
		used := 0
		for _, r := range raw {
			w := Advance(r)
			if used+w > cols {
				lines = append(lines, line)
				line = nil
				used = 0
			}
			if string(r) == "\uFFFD" {
				r = '?'
			}
			line = append(line, cell{r: r, half: used})
			used += w
		}
		lines = append(lines, line)
	}
	return lines
}

// Render converts text into PNG frames.
func Render(text string, opt Options) ([]Frame, error) {
	if opt.FrameWidth <= 0 {
		opt.FrameWidth = 1568
	}
	if opt.MaxFrameHeight <= 0 {
		opt.MaxFrameHeight = 2048
	}
	cols := opt.FrameWidth / HalfColumn
	if cols < 1 {
		cols = 1
	}
	lines := layout(Normalize(text), cols)
	rowsPerFrame := (opt.MaxFrameHeight - Padding) / CellH
	if rowsPerFrame < 1 {
		rowsPerFrame = 1
	}
	var frames []Frame
	for start := 0; start < len(lines); start += rowsPerFrame {
		end := start + rowsPerFrame
		if end > len(lines) {
			end = len(lines)
		}
		chunk := lines[start:end]
		height := len(chunk)*CellH + Padding
		img := image.NewGray(image.Rect(0, 0, opt.FrameWidth, height))
		for i := range img.Pix {
			img.Pix[i] = 0xFF
		}
		for row, line := range chunk {
			y := Padding/2 + row*CellH
			for _, c := range line {
				drawGlyph(img, c.r, c.half*HalfColumn, y)
			}
		}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
		frames = append(frames, Frame{PNG: buf.Bytes(), Width: opt.FrameWidth, Height: height, Rows: len(chunk)})
	}
	if len(frames) == 0 {
		img := image.NewGray(image.Rect(0, 0, opt.FrameWidth, Padding))
		for i := range img.Pix {
			img.Pix[i] = 0xFF
		}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
		frames = append(frames, Frame{PNG: buf.Bytes(), Width: opt.FrameWidth, Height: Padding})
	}
	return frames, nil
}

func drawGlyph(img *image.Gray, r rune, x, y int) {
	bits := Glyph(r)
	if bits == nil {
		return
	}
	for gy := range CellH {
		for gx := range CellW {
			idx := gy*(CellW/8) + gx/8
			if idx >= len(bits) {
				continue
			}
			if bits[idx]&(1<<(7-uint(gx%8))) == 0 {
				continue
			}
			px, py := x+gx, y+gy
			if px < 0 || py < 0 || px >= img.Rect.Dx() || py >= img.Rect.Dy() {
				continue
			}
			img.SetGray(px, py, color.Gray{Y: 0})
		}
	}
}
