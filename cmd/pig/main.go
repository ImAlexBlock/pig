// Command pig is the single-binary personal assistant.
package main

import (
	"os"

	"github.com/alexblock/pig/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
