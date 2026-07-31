package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

// defaultToolTimeout is the timeout applied to MCP tool calls when the LLM does
// not provide an explicit timeout_seconds argument.
const defaultMCPToolTimeoutSeconds = 120

// Executable wraps an MCP tool as an Lcoder tool.Executable.
type Executable struct {
	client        *Client
	tool          Tool
	injectTimeout bool
}

// NewExecutable creates an executable wrapper for an MCP tool.
func NewExecutable(client *Client, tool Tool) *Executable {
	return &Executable{
		client:        client,
		tool:          tool,
		injectTimeout: !schemaHasProperty(tool.InputSchema, "timeout_seconds"),
	}
}

// Definition returns the tool schema exposed to the LLM.
func (e *Executable) Definition() models.ToolDefinition {
	params := e.tool.InputSchema
	if e.injectTimeout {
		params = injectTimeoutProperty(params)
	}
	return models.ToolDefinition{
		Name:        PrefixedName(e.client.Name(), e.tool.Name),
		Description: fmt.Sprintf("[%s] %s", e.client.Name(), e.tool.Description),
		Parameters:  params,
	}
}

// Execute invokes the MCP tool.
func (e *Executable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	timeout := defaultMCPToolTimeoutSeconds
	toolArgs := args

	if e.injectTimeout {
		if v, ok := args["timeout_seconds"].(float64); ok {
			timeout = int(v)
		}
		// Remove the LLM-control parameter before sending it to the MCP server;
		// it is an Lcoder concern, not a server tool argument.
		if len(args) > 0 {
			toolArgs = make(map[string]any, len(args))
			for k, v := range args {
				if k != "timeout_seconds" {
					toolArgs[k] = v
				}
			}
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	result, err := e.client.CallTool(callCtx, e.tool.Name, toolArgs)
	if err != nil {
		return models.NewToolExecutionResultError(err.Error()), nil
	}

	content := make([]models.ContentPart, 0, len(result.Content))
	for _, item := range result.Content {
		switch item.Type {
		case "image":
			content = append(content, models.ImageContent{Data: item.Data, MimeType: item.MimeType})
		default:
			content = append(content, models.TextContent{Text: item.Text})
		}
	}

	if result.IsError {
		return models.NewToolExecutionResultError(result.ContentText()), nil
	}
	return models.ToolExecutionResult{Content: content}, nil
}

// schemaHasProperty reports whether the JSON schema already defines the named
// property. We avoid injecting a conflicting parameter when the server already
// exposes its own timeout_seconds.
func schemaHasProperty(schema map[string]any, name string) bool {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = props[name]
	return ok
}

// injectTimeoutProperty returns a shallow copy of the schema with an optional
// timeout_seconds integer property added to the properties map. The original
// required array is left unchanged so timeout_seconds remains optional.
func injectTimeoutProperty(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = v
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = make(map[string]any)
		out["properties"] = props
	} else {
		newProps := make(map[string]any, len(props)+1)
		for k, v := range props {
			newProps[k] = v
		}
		out["properties"] = newProps
		props = newProps
	}

	props["timeout_seconds"] = map[string]any{
		"type":        "integer",
		"description": fmt.Sprintf("Timeout for this tool call in seconds (default %d)", defaultMCPToolTimeoutSeconds),
	}
	return out
}

// ContentText is a helper to extract text from CallToolResult content.
func (r *CallToolResult) ContentText() string {
	var out string
	for _, item := range r.Content {
		out += item.Text
	}
	return out
}
