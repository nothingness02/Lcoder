// pkg/llm/client.go
package llm

import (
	"context"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// Client is the in-process LLM client. It keeps the method surface the agent
// loop and TUI depend on, delegating to an in-process engine.
type Client struct {
	engine *engine.Engine
}

// NewClient creates a client over an in-process engine.
func NewClient(eng *engine.Engine) *Client {
	return &Client{engine: eng}
}

// StreamTurn starts a provider turn and returns the normalized event stream.
func (c *Client) StreamTurn(ctx context.Context, req models.TurnRequest) (<-chan provider.Event, error) {
	return c.engine.StreamTurn(ctx, req)
}

// RegisterProvider stores a provider connection on the engine (in-process).
// An explicitly set Protocol is validated; empty derives from the route.
func (c *Client) RegisterProvider(ctx context.Context, name string, conn config.ProviderConn) error {
	var proto provider.Protocol
	if conn.Protocol != "" {
		p, err := provider.ParseProtocol(conn.Protocol)
		if err != nil {
			return err
		}
		proto = p
	}
	c.engine.RegisterProvider(name, provider.Conn{
		BaseURL:       conn.BaseURL,
		APIKey:        conn.APIKey,
		APIKeys:       conn.APIKeys,
		Route:         conn.Route,
		Protocol:      proto,
		Headers:       conn.Headers,
		MaxConcurrent: conn.MaxConcurrent,
	})
	return nil
}

// ListModels returns the available models from the catalog.
func (c *Client) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	return c.engine.ListModels(), nil
}

// ModelWindow returns the catalog context window for provider/model (0 if unknown).
func (c *Client) ModelWindow(ctx context.Context, prov, model string) (int, error) {
	return c.engine.ModelWindow(prov, model), nil
}

// ModelMaxOutput returns the catalog single-response output ceiling for
// provider/model (0 if unknown).
func (c *Client) ModelMaxOutput(ctx context.Context, prov, model string) (int, error) {
	return c.engine.ModelMaxOutput(prov, model), nil
}

// ModelMaxInput returns the model prompt cap (0 = use the context window).
func (c *Client) ModelMaxInput(ctx context.Context, prov, model string) (int, error) {
	return c.engine.ModelMaxInput(prov, model), nil
}

// ResolveThinking validates the configured thinking value; see engine.
func (c *Client) ResolveThinking(ctx context.Context, provider, model, want string) (string, string) {
	return c.engine.ResolveThinking(provider, model, want)
}

// Health reports in-process readiness.
func (c *Client) Health(ctx context.Context) (map[string]string, error) {
	return map[string]string{"status": "ok"}, nil
}
