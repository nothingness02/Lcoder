package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout    = 10 * time.Second
	defaultSearchPath     = "/search"
	defaultObservePath    = "/observe"
	defaultSessionEndPath = "/session/end"
)

// HTTPProviderConfig configures a generic HTTP/REST external memory provider.
type HTTPProviderConfig struct {
	Endpoint       string            `yaml:"endpoint"`
	APIKey         string            `yaml:"api_key"`
	Headers        map[string]string `yaml:"headers"`
	Timeout        int               `yaml:"timeout"`
	SearchPath     string            `yaml:"search_path"`
	ObservePath    string            `yaml:"observe_path"`
	SessionEndPath string            `yaml:"session_end_path"`
}

// HTTPProvider is a Provider backed by a generic HTTP/REST memory service.
type HTTPProvider struct {
	cfg     HTTPProviderConfig
	client  *http.Client
	breaker *circuitBreaker
}

// NewHTTPProvider creates a new HTTP-backed memory provider.
func NewHTTPProvider(cfg HTTPProviderConfig) *HTTPProvider {
	if cfg.Timeout <= 0 {
		cfg.Timeout = int(defaultHTTPTimeout.Seconds())
	}
	if cfg.SearchPath == "" {
		cfg.SearchPath = defaultSearchPath
	}
	if cfg.ObservePath == "" {
		cfg.ObservePath = defaultObservePath
	}
	if cfg.SessionEndPath == "" {
		cfg.SessionEndPath = defaultSessionEndPath
	}
	return &HTTPProvider{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		breaker: newCircuitBreaker(defaultBreakerThreshold, defaultBreakerResetTimeout),
	}
}

// WithBreaker replaces the provider's circuit breaker. Intended for tests.
func (p *HTTPProvider) WithBreaker(b *circuitBreaker) *HTTPProvider {
	p.breaker = b
	return p
}

// Healthy reports whether the underlying circuit breaker allows traffic.
func (p *HTTPProvider) Healthy(ctx context.Context) bool {
	return p.breaker.allow()
}

// Prefetch fetches relevant memories from the configured search endpoint.
func (p *HTTPProvider) Prefetch(ctx context.Context, query string) ([]string, error) {
	body, err := json.Marshal(prefetchRequest{Query: query})
	if err != nil {
		return nil, err
	}

	resp, err := p.post(ctx, p.cfg.SearchPath, body)
	if err != nil {
		p.breaker.recordFailure()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		p.breaker.recordFailure()
		return nil, fmt.Errorf("memory search returned status %d", resp.StatusCode)
	}

	var decoded prefetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		p.breaker.recordFailure()
		return nil, err
	}

	p.breaker.recordSuccess()
	return decoded.Memories, nil
}

// SyncTurn records a completed turn at the observe endpoint.
func (p *HTTPProvider) SyncTurn(ctx context.Context, user, assistant string) error {
	body, err := json.Marshal(syncTurnRequest{User: user, Assistant: assistant})
	if err != nil {
		return err
	}

	resp, err := p.post(ctx, p.cfg.ObservePath, body)
	if err != nil {
		p.breaker.recordFailure()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		p.breaker.recordFailure()
		return fmt.Errorf("memory observe returned status %d", resp.StatusCode)
	}

	p.breaker.recordSuccess()
	return nil
}

// OnSessionEnd notifies the provider that the session has ended.
func (p *HTTPProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	body, err := json.Marshal(sessionEndRequest{SessionID: summary.SessionID, TurnCount: summary.TurnCount})
	if err != nil {
		return err
	}

	resp, err := p.post(ctx, p.cfg.SessionEndPath, body)
	if err != nil {
		p.breaker.recordFailure()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		p.breaker.recordFailure()
		return fmt.Errorf("memory session end returned status %d", resp.StatusCode)
	}

	p.breaker.recordSuccess()
	return nil
}

func (p *HTTPProvider) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := strings.TrimSuffix(p.cfg.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
	return p.client.Do(req)
}

type prefetchRequest struct {
	Query string `json:"query"`
}

type prefetchResponse struct {
	Memories []string `json:"memories"`
}

type syncTurnRequest struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type sessionEndRequest struct {
	SessionID string `json:"session_id"`
	TurnCount int    `json:"turn_count"`
}
