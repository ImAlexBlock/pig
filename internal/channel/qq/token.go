// Package qq implements the QQ official bot channel (API v2).
package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Token caches the QQ app access token.
type Token struct {
	APIBase    string
	AppID      string
	AppSecret  string
	HTTPClient *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Get returns a valid access token, refreshing at 80% of its lifetime.
func (t *Token) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expires) {
		return t.token, nil
	}
	bases := []string{t.APIBase, "https://bots.qq.com"}
	var lastErr error
	for _, base := range bases {
		if base == "" {
			continue
		}
		tok, ttl, err := t.fetch(ctx, base)
		if err != nil {
			lastErr = err
			continue
		}
		t.token = tok
		t.expires = time.Now().Add(time.Duration(float64(ttl) * 0.8 * float64(time.Second)))
		return tok, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no api base configured")
	}
	return "", lastErr
}

func (t *Token) fetch(ctx context.Context, base string) (string, int, error) {
	body, _ := json.Marshal(map[string]string{"appId": t.AppID, "clientSecret": t.AppSecret})
	url := base + "/app/getAppAccessToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("content-type", "application/json")
	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("qq token: status %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, err
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("qq token: empty token")
	}
	ttl := 7200
	var asInt int
	if err := json.Unmarshal(out.ExpiresIn, &asInt); err == nil {
		ttl = asInt
	} else {
		var asStr string
		if err := json.Unmarshal(out.ExpiresIn, &asStr); err == nil {
			fmt.Sscanf(asStr, "%d", &ttl)
		}
	}
	return out.AccessToken, ttl, nil
}
