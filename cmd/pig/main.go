// Command pig is the single-binary personal assistant.
package main

import (
	"os"

	"github.com/ImAlexBlock/pig/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
