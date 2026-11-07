package cli

import (
	"fmt"

	"github.com/alexblock/pig/docs"
)

func (a *App) cmdDocs(args []string) (int, error) {
	if len(args) == 0 {
		for _, topic := range docs.Topics() {
			fmt.Fprintln(a.Out, topic)
		}
		return ExitOK, nil
	}
	topic := args[0]
	if topic == "skills" {
		topic = "skill-authoring"
	}
	body, ok := docs.Get(topic)
	if !ok {
		return ExitUsage, usagef("docs: unknown topic %q", args[0])
	}
	fmt.Fprint(a.Out, body)
	return ExitOK, nil
}
