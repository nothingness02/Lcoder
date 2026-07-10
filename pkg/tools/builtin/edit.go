package builtin

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Edit performs exact-text replacements in a file.
type Edit struct {
	cwd string
	sb  sandbox.Sandbox
}

// UseSandbox injects the sandbox used to enforce filesystem checks.
func (e *Edit) UseSandbox(sb sandbox.Sandbox) { e.sb = sb }

// NewEdit creates an edit tool.
func NewEdit(cwd string) tools.Executable {
	return &Edit{cwd: cwd}
}

func (e *Edit) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "edit",
		Description: "Edit a single file using exact text replacement. Each oldText must match a unique, non-overlapping region.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to edit",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "One or more targeted replacements",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"oldText": map[string]any{
								"type":        "string",
								"description": "Exact text to replace",
							},
							"newText": map[string]any{
								"type":        "string",
								"description": "Replacement text",
							},
						},
						"required": []string{"oldText", "newText"},
					},
				},
			},
			"required": []string{"path", "edits"},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

const (
	backupSuffix = ".lcoder.bak"
	tmpSuffix    = ".lcoder.tmp"
)

type editOp struct {
	oldText string
	newText string
}

func parseEdits(args map[string]any) ([]editOp, error) {
	editsRaw, ok := args["edits"].([]any)
	if !ok || len(editsRaw) == 0 {
		return nil, fmt.Errorf("missing edits")
	}
	out := make([]editOp, 0, len(editsRaw))
	for _, raw := range editsRaw {
		edit, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid edit entry")
		}
		oldText, ok := edit["oldText"].(string)
		if !ok {
			return nil, fmt.Errorf("edit missing oldText")
		}
		newText, ok := edit["newText"].(string)
		if !ok {
			return nil, fmt.Errorf("edit missing newText")
		}
		out = append(out, editOp{oldText: oldText, newText: newText})
	}
	return out, nil
}

func applyEdits(text string, edits []editOp) (string, error) {
	for _, e := range edits {
		if !containsOnce(text, e.oldText) {
			return "", fmt.Errorf("oldText not found or not unique")
		}
		text = replaceOnce(text, e.oldText, e.newText)
	}
	return text, nil
}

func (e *Edit) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := args["path"].(string)
	path, err := resolveAndCheck(e.cwd, e.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	edits, err := parseEdits(args)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	// Stage 1: dry-run in memory.
	newText, err := applyEdits(string(original), edits)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("%s: %w", path, err)
	}

	// Stage 2: commit with backup + atomic rename.
	backupPath := path + backupSuffix
	if err := os.WriteFile(backupPath, original, 0o600); err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("backup failed: %w", err)
	}

	tmpPath := path + tmpSuffix
	if err := os.WriteFile(tmpPath, []byte(newText), 0o644); err != nil {
		_ = os.Remove(backupPath)
		return models.ToolExecutionResult{}, fmt.Errorf("write temp failed: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		_ = os.Remove(tmpPath)
		return models.ToolExecutionResult{}, fmt.Errorf("commit failed: %w; restored from backup", err)
	}

	_ = os.Remove(backupPath)

	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Applied %d edit(s) to %s", len(edits), path)},
		},
		Details: map[string]any{"path": path, "edits": len(edits)},
	}, nil
}

func containsOnce(s, substr string) bool {
	if substr == "" {
		return false
	}
	return strings.Count(s, substr) == 1
}

func replaceOnce(s, old, new string) string {
	if old == "" {
		return s
	}
	return strings.Replace(s, old, new, 1)
}

var _ tools.Executable = (*Edit)(nil)
