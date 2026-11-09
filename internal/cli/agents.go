package cli

import (
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/internalurl"
	"github.com/ImAlexBlock/pig/internal/store"
)

func (a *App) cmdAgents(args []string) (int, error) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	st, err := a.openStore()
	if err != nil {
		return ExitStorage, err
	}
	defer st.Close()
	switch args[0] {
	case "list":
		sessions, err := st.ListSessions(200)
		if err != nil {
			return ExitError, err
		}
		var children []*store.Session
		for _, s := range sessions {
			if s.ParentID != "" {
				children = append(children, s)
			}
		}
		if a.JSON {
			a.printJSON(children)
			return ExitOK, nil
		}
		for _, s := range children {
			fmt.Fprintf(a.Out, "%s\tparent=%s\tdepth=%d\t%s\n", s.ID, s.ParentID, s.Depth, strings.TrimSpace(s.Title))
		}
		return ExitOK, nil
	case "show":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig agents show <id>")
		}
		sess, err := st.GetSession(args[1])
		if err != nil {
			return ExitError, err
		}
		entries, err := st.TailEntries(sess.ID, 50)
		if err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(map[string]any{"session": sess, "entries": entries})
			return ExitOK, nil
		}
		fmt.Fprintf(a.Out, "%s parent=%s depth=%d\n\n", sess.ID, sess.ParentID, sess.Depth)
		for _, e := range entries {
			fmt.Fprintln(a.Out, internalurl.RenderEntry(e))
		}
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown agents action %q", args[0])
}
