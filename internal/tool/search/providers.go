package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const userAgent = "Mozilla/5.0 (compatible; Pig/1.0)"

var httpClient = &http.Client{Timeout: 10 * time.Second}

func getJSON(ctx context.Context, method, urlStr string, headers map[string]string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		return err
	}
	req.Header.Set("user-agent", userAgent)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", urlStr, resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// duckduckgo scrapes the HTML endpoint.
func duckduckgo(ctx context.Context, query string, count int) ([]Result, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: status %d", resp.StatusCode)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out []Result
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= count {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result__a") {
			title := strings.TrimSpace(textOf(n))
			href := attr(n, "href")
			if strings.HasPrefix(href, "//duckduckgo.com/l/?uddg=") {
				if idx := strings.Index(href, "uddg="); idx >= 0 {
					raw := href[idx+len("uddg="):]
					if amp := strings.Index(raw, "&"); amp >= 0 {
						raw = raw[:amp]
					}
					if dec, derr := url.QueryUnescape(raw); derr == nil {
						href = dec
					}
				}
			}
			snippet := ""
			if parent := ancestor(n, "result"); parent != nil {
				if sn := findByClass(parent, "result__snippet"); sn != nil {
					snippet = strings.TrimSpace(textOf(sn))
				}
			}
			out = append(out, Result{Title: title, URL: href, Snippet: snippet})
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
			if len(out) >= count {
				return
			}
		}
	}
	walk(doc)
	return out, nil
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, f := range strings.Fields(a.Val) {
				if f == class {
					return true
				}
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func ancestor(n *html.Node, class string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && hasClass(p, class) {
			return p
		}
	}
	return nil
}

func findByClass(n *html.Node, class string) *html.Node {
	if n.Type == html.ElementNode && hasClass(n, class) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findByClass(c, class); got != nil {
			return got
		}
	}
	return nil
}

func brave(ctx context.Context, query string, count int) ([]Result, error) {
	key := os.Getenv("BRAVE_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("missing BRAVE_API_KEY")
	}
	var resp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), count)
	if err := getJSON(ctx, http.MethodGet, u, map[string]string{"X-Subscription-Token": key}, nil, &resp); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range resp.Web.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: stripTags(r.Description)})
	}
	return out, nil
}

func tavily(ctx context.Context, query string, count int) ([]Result, error) {
	key := os.Getenv("TAVILY_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("missing TAVILY_API_KEY")
	}
	var resp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	body := map[string]any{"api_key": key, "query": query, "max_results": count}
	if err := getJSON(ctx, http.MethodPost, "https://api.tavily.com/search", nil, body, &resp); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range resp.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

func exa(ctx context.Context, query string, count int) ([]Result, error) {
	key := os.Getenv("EXA_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("missing EXA_API_KEY")
	}
	var resp struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
	}
	body := map[string]any{"query": query, "numResults": count, "contents": map[string]any{"text": true}}
	if err := getJSON(ctx, http.MethodPost, "https://api.exa.ai/search", map[string]string{"x-api-key": key}, body, &resp); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range resp.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Text})
	}
	return out, nil
}

func serper(ctx context.Context, query string, count int) ([]Result, error) {
	key := os.Getenv("SERPER_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("missing SERPER_API_KEY")
	}
	var resp struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	body := map[string]any{"q": query, "num": count}
	if err := getJSON(ctx, http.MethodPost, "https://google.serper.dev/search", map[string]string{"X-API-KEY": key}, body, &resp); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range resp.Organic {
		out = append(out, Result{Title: r.Title, URL: r.Link, Snippet: r.Snippet})
	}
	return out, nil
}

func stripTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	return strings.TrimSpace(textOf(doc))
}
