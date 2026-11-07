package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alexblock/pig/internal/compact/snapcompact"
)

func (a *App) cmdDebug(args []string) (int, error) {
	if len(args) == 0 || args[0] != "render-snap" {
		return ExitUsage, usagef("usage: pig debug render-snap --input <file> --out <dir>")
	}
	fs := flag.NewFlagSet("debug render-snap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "", "input text file")
	out := fs.String("out", "", "output directory")
	width := fs.Int("width", 1568, "frame width")
	maxHeight := fs.Int("max-height", 2048, "max frame height")
	if err := fs.Parse(args[1:]); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	if *input == "" || *out == "" {
		return ExitUsage, usagef("render-snap: --input and --out are required")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return ExitError, fmt.Errorf("render-snap: %v", err)
	}
	frames, err := snapcompact.Render(string(data), snapcompact.Options{
		FrameWidth: *width, MaxFrameHeight: *maxHeight,
	})
	if err != nil {
		return ExitError, fmt.Errorf("render-snap: %v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return ExitError, fmt.Errorf("render-snap: %v", err)
	}
	for i, f := range frames {
		path := filepath.Join(*out, fmt.Sprintf("frame-%03d.png", i))
		if err := os.WriteFile(path, f.PNG, 0o644); err != nil {
			return ExitError, fmt.Errorf("render-snap: %v", err)
		}
		fmt.Fprintf(a.Out, "%s\t%dx%d\n", path, f.Width, f.Height)
	}
	return ExitOK, nil
}
