package provider

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/alexblock/pig/internal/config"
)

// Registry builds wire clients from configuration.
type Registry struct {
	cfg     *config.Config
	client  *http.Client
	mu      sync.Mutex
	clients map[string]Client
}

// NewRegistry returns a registry bound to cfg.
func NewRegistry(cfg *config.Config) *Registry {
	return &Registry{cfg: cfg, client: HTTPClient(), clients: map[string]Client{}}
}

// Client returns (building on first use) the client for a provider name.
func (r *Registry) Client(name string) (Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[name]; ok {
		return c, nil
	}
	pc, ok := r.cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider: unknown provider %q", name)
	}
	key := ""
	if pc.APIKeyEnv != "" {
		key = os.Getenv(pc.APIKeyEnv)
	}
	base := strings.TrimSuffix(pc.BaseURL, "/")
	var c Client
	switch pc.Kind {
	case "anthropic":
		if key == "" {
			return nil, fmt.Errorf("provider %s: missing %s", name, pc.APIKeyEnv)
		}
		c = &anthropicClient{name: name, base: base, key: key, http: r.client}
	case "openai-chat":
		if key == "" {
			return nil, fmt.Errorf("provider %s: missing %s", name, pc.APIKeyEnv)
		}
		c = &chatClient{name: name, base: base, key: key, http: r.client}
	case "openai-responses":
		if key == "" {
			return nil, fmt.Errorf("provider %s: missing %s", name, pc.APIKeyEnv)
		}
		c = &responsesClient{name: name, base: base, key: key, http: r.client}
	case "mock":
		c = newMockClient()
	default:
		return nil, fmt.Errorf("provider %s: unknown kind %q", name, pc.Kind)
	}
	r.clients[name] = c
	return c, nil
}

// Close drops cached clients.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients = map[string]Client{}
}
