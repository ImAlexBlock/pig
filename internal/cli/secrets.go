package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// readSecret reads a secret from in, hiding echo when in is a terminal.
func readSecret(in io.Reader, errW io.Writer, name string) (string, error) {
	var f *os.File
	if fh, ok := in.(*os.File); ok {
		f = fh
	}
	if f != nil && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(errW, "%s: ", name)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(errW)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
