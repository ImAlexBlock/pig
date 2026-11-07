package cli

import (
	"os"

	"github.com/alexblock/pig/internal/rtk"
)

func ensureDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// rtkApply exposes RTK filtering to the CLI.
func rtkApply(command, stdout string, exitCode int) (string, string, int) {
	return rtk.Apply(command, stdout, exitCode)
}
