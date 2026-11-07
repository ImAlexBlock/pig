// Package docs embeds the user documentation served by `pig docs`.
package docs

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// Topics lists the embedded documents without the .md suffix.
func Topics() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

// Get returns one document body.
func Get(topic string) (string, bool) {
	b, err := files.ReadFile(topic + ".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}
