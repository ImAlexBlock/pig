package cli

import (
	"flag"
	"fmt"
	"io"
)

func (a *App) cmdRTK(args []string) (int, error) {
	if len(args) == 0 {
		return ExitUsage, usagef("usage: pig rtk preview|gain")
	}
	switch args[0] {
	case "preview":
		fs := flag.NewFlagSet("rtk preview", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		command := fs.String("command", "", "command the output came from")
		exitCode := fs.Int("exit", 0, "exit code to simulate")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		in, err := io.ReadAll(a.In)
		if err != nil {
			return ExitError, err
		}
		out, rule, saved := rtkApply(*command, string(in), *exitCode)
		fmt.Fprintln(a.Out, out)
		fmt.Fprintf(a.Err, "pig: rtk rule=%q saved=%d bytes\n", rule, saved)
		return ExitOK, nil
	case "gain":
		st, err := a.openStore()
		if err != nil {
			return ExitStorage, err
		}
		defer st.Close()
		g, err := st.RTKGain()
		if err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(g)
			return ExitOK, nil
		}
		var pct float64
		if g.RawBytes > 0 {
			pct = float64(g.SavedBytes) * 100 / float64(g.RawBytes)
		}
		fmt.Fprintf(a.Out, "calls: %d\nraw: %d bytes\nfiltered: %d bytes\nsaved: %d bytes (%.1f%%)\n",
			g.Calls, g.RawBytes, g.FilteredBytes, g.SavedBytes, pct)
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown rtk action %q", args[0])
}
