package cli

import (
	"fmt"

	"github.com/alexblock/pig/internal/internalurl"
	"github.com/alexblock/pig/internal/skill"
)

func (a *App) cmdShow(args []string) (int, error) {
	if len(args) < 1 {
		return ExitUsage, usagef("usage: pig show <uri>")
	}
	st, err := a.openStore()
	if err != nil {
		return ExitStorage, err
	}
	defer st.Close()
	_ = a.loadConfig()
	cfg := a.Config
	home := a.Paths.Home
	reg := skill.Discover(cfg, home, a.CWD)
	r := &internalurl.Resolver{Store: st, SessionID: "", SkillDir: reg.Dir}
	doc, err := r.Open(args[0])
	if err != nil {
		return ExitError, err
	}
	switch doc.Kind {
	case internalurl.KindText, internalurl.KindDir:
		text, first := internalurl.SelectLines(doc.Text, doc.Start, doc.End)
		fmt.Fprintln(a.Out, internalurl.RenderLinePrefixed(text, first, doc.Raw))
	case internalurl.KindBinary:
		fmt.Fprintf(a.Out, "binary file (%d bytes)\n", doc.Bytes)
	default:
		fmt.Fprintf(a.Out, "image %s (%d bytes)\n", doc.Mime, doc.Bytes)
	}
	return ExitOK, nil
}
