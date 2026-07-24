package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/lcoder/lcoder/pkg/models"
)

// HTTPConfig describes an external HTTP tool.
type HTTPConfig struct {
	Name          string               `json:"name"`
	Endpoint      string               `json:"endpoint"`
	Description   string               `json:"description"`
	Parameters    map[string]any       `json:"parameters"`
	ExecutionMode models.ExecutionMode `json:"execution_mode"`
	Headers       map[string]string    `json:"headers"`
}

// HTTPExecutable calls a remote HTTP endpoint for a tool.
type HTTPExecutable struct {
	cfg    HTTPConfig
	client *http.Client
}

// NewHTTPExecutable creates an HTTP tool executable.
func NewHTTPExecutable(cfg HTTPConfig) *HTTPExecutable {
	return &HTTPExecutable{cfg: cfg, client: &http.Client{}}
}

// Definition returns the tool schema exposed to the LLM.
func (h *HTTPExecutable) Definition() models.ToolDefinition {
	mode := h.cfg.ExecutionMode
	if mode == "" {
		mode = models.ExecutionParallel
	}
	return models.ToolDefinition{
		Name:          h.cfg.Name,
		Description:   h.cfg.Description,
		Parameters:    h.cfg.Parameters,
		ExecutionMode: mode,
	}
}

// maxHTTPResponseBytes caps how much of an endpoint's response body is read
// into the conversation; oversized bodies are truncated with a notice instead
// of being parsed (a truncated JSON payload is not decodable anyway).
const maxHTTPResponseBytes = 32 * 1024

// Execute sends a tool call to the configured HTTP endpoint.
func (h *HTTPExecutable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	cwd, _ := os.Getwd()
	payload := map[string]any{
		"tool_call_id": callID,
		"name":         h.cfg.Name,
		"arguments":    args,
		"context": map[string]any{
			"cwd": cwd,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lcoder/0.1.0")
	for k, v := range h.cfg.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if len(respBody) > maxHTTPResponseBytes {
		result := models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{
				Text: fmt.Sprintf("%s\n\n[truncated: response body exceeded %d bytes]", respBody[:maxHTTPResponseBytes], maxHTTPResponseBytes),
			}},
			Details: map[string]any{"status_code": resp.StatusCode, "truncated": true},
			IsError: resp.StatusCode >= 400,
		}
		return result, nil
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error   string         `json:"error"`
			Details map[string]any `json:"details"`
		}
		if err := json.Unmarshal(respBody, &errResp); err == nil && errResp.Error != "" {
			result := models.NewToolExecutionResultError(errResp.Error)
			result.Details = map[string]any{"status_code": resp.StatusCode}
			return result, nil
		}
		result := models.NewToolExecutionResultError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)))
		result.Details = map[string]any{"status_code": resp.StatusCode}
		return result, nil
	}

	var success struct {
		Content   []contentPartEnv `json:"content"`
		Details   map[string]any   `json:"details"`
		Terminate bool             `json:"terminate"`
	}
	if err := json.Unmarshal(respBody, &success); err != nil {
		return models.NewToolExecutionResultError(fmt.Sprintf("invalid tool response: %s", string(respBody))), nil
	}

	content := make([]models.ContentPart, 0, len(success.Content))
	for _, c := range success.Content {
		part := c.toContentPart()
		if part != nil {
			content = append(content, part)
		}
	}
	if len(content) == 0 {
		content = append(content, models.TextContent{Text: string(respBody)})
	}

	return models.ToolExecutionResult{
		Content:   content,
		Details:   success.Details,
		Terminate: success.Terminate,
	}, nil
}

type contentPartEnv struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

func (c contentPartEnv) toContentPart() models.ContentPart {
	switch c.Type {
	case "text":
		return models.TextContent{Text: c.Text}
	case "image":
		return models.ImageContent{Data: c.Data, MimeType: c.MimeType}
	default:
		return models.TextContent{Text: c.Text}
	}
}

var _ Executable = (*HTTPExecutable)(nil)

// RegisterHTTP registers one or more HTTP tools from config.
func RegisterHTTP(registry *Registry, configs []HTTPConfig) {
	for _, cfg := range configs {
		registry.Register(cfg.Name, NewHTTPExecutable(cfg))
	}
}

// ExpandEndpointEnv expands ${VAR} references in endpoint strings.
func ExpandEndpointEnv(endpoint string) string {
	return os.Expand(endpoint, func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return "${" + key + "}"
	})
}
