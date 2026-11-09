package cli

import (
	"fmt"

	"github.com/ImAlexBlock/pig/internal/skill"
)

func (a *App) cmdSkills(args []string) (int, error) {
	_ = a.loadConfig()
	cfg := a.Config
	reg := skill.Discover(cfg, a.Paths.Home, a.CWD)
	if len(args) > 0 && args[0] != "list" {
		return ExitUsage, usagef("unknown skills action %q", args[0])
	}
	if a.JSON {
		a.printJSON(reg.List())
		return ExitOK, nil
	}
	for _, sk := range reg.List() {
		fmt.Fprintf(a.Out, "%s\t%s\t%s\n", sk.Name, sk.Description, sk.Dir)
	}
	for _, w := range reg.Warnings() {
		fmt.Fprintf(a.Err, "pig: %s\n", w)
	}
	return ExitOK, nil
}
