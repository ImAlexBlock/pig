// Package search implements the web search tool with multiple providers.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/tool"
)

const searchSchema = `{
  "type": "object",
  "properties": {
    "query":       {"type": "string", "description": "Search query, or a regex when path/file_glob is set."},
    "path":        {"type": "string", "description": "Search the filesystem under this directory instead of the web."},
    "file_glob":   {"type": "string", "description": "Filename filter for filesystem search, e.g. **/*.py."},
    "output_mode": {"type": "string", "enum": ["content", "files"], "description": "filesystem search: matching lines or file names."},
    "count":       {"type": "integer", "description": "Maximum number of results."},
    "fetch":       {"type": "integer", "description": "Fetch and extract this many result pages (web only)."}
  },
  "required": ["query"],
  "additionalProperties": false
}`

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Tool is the search tool.
type Tool struct {
	cfg *config.Config
}

// NewTool returns a search tool.
func NewTool(cfg *config.Config) *Tool { return &Tool{cfg: cfg} }

func (t *Tool) Name() string { return "search" }
func (t *Tool) Description() string {
	return "Search the web and optionally fetch result pages as text."
}
func (t *Tool) Schema() json.RawMessage {
	return json.RawMessage(searchSchema)
}
func (t *Tool) ReadOnly() bool { return true }

func (t *Tool) Run(ctx context.Context, c tool.Call, env *tool.Env) (tool.Result, error) {
	var a struct {
		Query      string `json:"query"`
		Path       string `json:"path"`
		FileGlob   string `json:"file_glob"`
		OutputMode string `json:"output_mode"`
		Count      int    `json:"count"`
		Fetch      int    `json:"fetch"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Error("search: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Query) == "" {
		return tool.Error("search: query is required"), nil
	}
	if a.Path != "" || a.FileGlob != "" || a.OutputMode != "" {
		return t.searchFilesystem(ctx, a.Query, a.Path, a.FileGlob, a.OutputMode, a.Count, env)
	}
	count := a.Count
	if count <= 0 {
		count = t.cfg.Search.MaxResults
	}
	if count <= 0 {
		count = 8
	}
	results, err := t.search(ctx, a.Query, count)
	if err != nil {
		return tool.Error("search: " + err.Error()), nil
	}
	if len(results) == 0 {
		return tool.Result{Text: "no results", Useless: true, Display: "search " + a.Query}, nil
	}
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n", i+1, strings.TrimSpace(r.Title), r.URL, strings.TrimSpace(r.Snippet))
	}
	fetch := a.Fetch
	if fetch <= 0 {
		fetch = t.cfg.Search.FetchPages
	}
	if fetch > len(results) {
		fetch = len(results)
	}
	for i := range fetch {
		page, err := t.fetchPage(ctx, results[i].URL)
		if err != nil || page == "" {
			continue
		}
		art := ""
		if env.Store != nil {
			if saved, serr := env.Store.PutArtifact(env.SessionID, "text/plain", []byte(page)); serr == nil {
				art = " [artifact://" + saved.ID + "]"
			}
		}
		fmt.Fprintf(&sb, "\n--- %s%s ---\n%s\n", results[i].URL, art, summarize(page))
	}
	return tool.Result{Text: sb.String(), Display: "search " + a.Query}, nil
}

func summarize(page string) string {
	if len(page) <= 1200 {
		return page
	}
	return page[:1200] + "…"
}

// search dispatches over the configured providers.
func (t *Tool) search(ctx context.Context, query string, count int) ([]Result, error) {
	providers := t.cfg.Search.Providers
	if len(providers) == 0 {
		providers = []string{"duckduckgo"}
	}
	if t.cfg.Search.Merge {
		return t.merge(ctx, providers, query, count)
	}
	var errs []string
	for _, name := range providers {
		res, err := t.one(ctx, name, query, count)
		if err != nil {
			errs = append(errs, name+": "+err.Error())
			continue
		}
		if len(res) > 0 {
			return res, nil
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil, nil
}

func (t *Tool) one(ctx context.Context, name, query string, count int) ([]Result, error) {
	switch name {
	case "duckduckgo":
		return duckduckgo(ctx, query, count)
	case "brave":
		return brave(ctx, query, count)
	case "tavily":
		return tavily(ctx, query, count)
	case "exa":
		return exa(ctx, query, count)
	case "serper":
		return serper(ctx, query, count)
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}

func (t *Tool) merge(ctx context.Context, providers []string, query string, count int) ([]Result, error) {
	type providerOut struct {
		res []Result
		err error
	}
	results := make([]providerOut, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			r, err := t.one(ctx, p, query, count)
			results[i] = providerOut{res: r, err: err}
		}(i, p)
	}
	wg.Wait()

	const k = 60
	scores := map[string]float64{}
	byURL := map[string]Result{}
	for _, o := range results {
		for rank, r := range o.res {
			scores[r.URL] += 1.0 / float64(k+rank+1)
			if _, ok := byURL[r.URL]; !ok {
				byURL[r.URL] = r
			}
		}
	}
	type scored struct {
		url   string
		score float64
	}
	list := make([]scored, 0, len(scores))
	for u, s := range scores {
		list = append(list, scored{u, s})
	}
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			if list[j].score > list[i].score {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	if len(list) > count {
		list = list[:count]
	}
	merged := make([]Result, 0, len(list))
	for _, s := range list {
		merged = append(merged, byURL[s.url])
	}
	return merged, nil
}
