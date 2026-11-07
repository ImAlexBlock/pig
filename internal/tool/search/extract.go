package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// fetchPage downloads a page and extracts readable text.
func (t *Tool) fetchPage(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("user-agent", userAgent)
	req.Header.Set("accept", "text/html,application/xhtml+xml")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch: status %d", resp.StatusCode)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	drop(doc, map[string]bool{
		"script": true, "style": true, "nav": true, "header": true,
		"footer": true, "aside": true, "iframe": true,
	})
	var sb strings.Builder
	extract(doc, &sb)
	text := collapse(sb.String())
	blocks := []string{}
	for _, line := range strings.Split(text, "\n") {
		if len(strings.TrimSpace(line)) < 30 {
			continue
		}
		blocks = append(blocks, strings.TrimSpace(line))
	}
	out := strings.Join(blocks, "\n")
	if len(out) > 20000 {
		out = out[:20000] + "\n…"
	}
	return out, nil
}

func drop(n *html.Node, tags map[string]bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && tags[c.Data] {
			n.RemoveChild(c)
		} else {
			drop(c, tags)
		}
		c = next
	}
}

func isBlock(tag string) bool {
	switch tag {
	case "p", "li", "pre", "h1", "h2", "h3", "div", "section", "article", "br", "tr":
		return true
	}
	return false
}

func extract(n *html.Node, sb *strings.Builder) {
	if n.Type == html.TextNode {
		sb.WriteString(n.Data)
		return
	}
	if n.Type == html.ElementNode && isBlock(n.Data) {
		sb.WriteString("\n")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extract(c, sb)
	}
	if n.Type == html.ElementNode && isBlock(n.Data) {
		sb.WriteString("\n")
	}
}

func collapse(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}
