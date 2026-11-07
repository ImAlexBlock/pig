package search

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/alexblock/pig/internal/tool"
)

// Filesystem search limits.
const (
	maxFSScanBytes  = 2 << 20
	maxFSResults    = 500
	maxFSLineLength = 240
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true,
	"venv": true, "dist": true, "build": true, "target": true,
	"__pycache__": true, ".idea": true, ".cache": true,
}

// searchFilesystem implements the filesystem mode of the search tool: a regex
// (or literal) query filtered by a filename glob, returning matching lines or
// file names.
func (t *Tool) searchFilesystem(ctx context.Context, query, root, fileGlob, mode string, count int, env *tool.Env) (tool.Result, error) {
	if root == "" {
		root = "."
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(env.CWD, root)
	}
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil {
		return tool.Error("search: path not found: " + root), nil
	} else if !info.IsDir() {
		return tool.Error("search: not a directory: " + root), nil
	}
	if fileGlob == "" {
		fileGlob = "**/*"
	}
	if mode == "" {
		mode = "content"
	}
	limit := count
	if limit <= 0 || limit > maxFSResults {
		limit = 200
	}
	re, reErr := regexp.Compile(query)
	if reErr != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(query))
	}

	var results []string
	var files []string
	scanned := 0
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if !globMatch(fileGlob, rel) {
			return nil
		}
		files = append(files, rel)
		if mode == "files" {
			if len(files) >= limit {
				return filepath.SkipAll
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxFSScanBytes {
			return nil
		}
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		scanned++
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), maxFSScanBytes)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			if len(line) > maxFSLineLength {
				line = line[:maxFSLineLength] + "…"
			}
			results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNo, strings.TrimRight(line, "\r")))
			if len(results) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != context.Canceled {
		return tool.Error("search: " + walkErr.Error()), nil
	}

	if mode == "files" {
		sort.Strings(files)
		if len(files) == 0 {
			return tool.Result{Text: "no files matched " + fileGlob, Useless: true, Display: "files"}, nil
		}
		if len(files) > limit {
			files = files[:limit]
		}
		return tool.Result{
			Text:    strings.Join(files, "\n"),
			Display: fmt.Sprintf("files %d", len(files)),
		}, nil
	}
	if len(results) == 0 {
		return tool.Result{
			Text:    fmt.Sprintf("no matches for %s in %d file(s) under %s", query, scanned, root),
			Useless: true,
			Display: "search 0",
		}, nil
	}
	return tool.Result{
		Text:    strings.Join(results, "\n"),
		Display: fmt.Sprintf("search %d", len(results)),
	}, nil
}

// globMatch supports `**/` prefixes and shell wildcards per path element.
func globMatch(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	rel = filepath.ToSlash(rel)
	if pattern == "**/*" || pattern == "*" {
		return true
	}
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	if rest, found := strings.CutPrefix(pattern, "**/"); found {
		if ok, _ := filepath.Match(rest, filepath.Base(rel)); ok {
			return true
		}
		for _, part := range strings.Split(rel, "/") {
			if ok, _ := filepath.Match(rest, part); ok {
				return true
			}
		}
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := filepath.Match(pattern, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}
