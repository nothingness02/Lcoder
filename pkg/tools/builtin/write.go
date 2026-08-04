package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Write writes content to a file.
type Write struct {
	cwd string
}

// NewWrite creates a write tool.
func NewWrite(cwd string) tools.Executable {
	return &Write{cwd: cwd}
}

func (w *Write) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "write",
		Description: "Write content to a file. Creates parent directories if needed and overwrites existing files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

// DeclareAccesses implements tools.AccessDeclarer: a write only touches its file.
func (w *Write) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return []tools.ToolAccess{{Op: tools.OpAll}}
	}
	return []tools.ToolAccess{{Op: tools.OpWrite, Path: resolveInCwd(w.cwd, path)}}
}

func (w *Write) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	content, ok := args["content"].(string)
	if !ok {
		return models.ToolExecutionResult{}, fmt.Errorf("missing or non-string argument \"content\"")
	}
	path = resolveInCwd(w.cwd, path)

	// See edit.go: rename would replace the symlink itself, so resolve the
	// link target before staging (only when the link already exists).
	if lst, err := os.Lstat(path); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return models.ToolExecutionResult{}, err
	}

	// Backup existing file before overwriting, and keep its permission bits.
	// tmp/backup names carry the call id so parallel writes to the same file
	// cannot clobber each other's staging files.
	var hadBackup bool
	var oldContent string
	mode := os.FileMode(0o644)
	backupPath := path + backupSuffix + "." + callID
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
		original, err := os.ReadFile(path)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("read existing file for backup: %w", err)
		}
		if err := os.WriteFile(backupPath, original, 0o600); err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("backup failed: %w", err)
		}
		hadBackup = true
		// Ship the previous content in the result details so the TUI can
		// render the overwrite as a diff. Capped to keep tool events small;
		// beyond the cap the TUI falls back to a plain content preview.
		if len(original) <= maxWriteDiffOldSize {
			oldContent = string(original)
		}
	}

	tmpPath := path + tmpSuffix + "." + callID
	if err := os.WriteFile(tmpPath, []byte(content), mode); err != nil {
		if hadBackup {
			_ = os.Remove(backupPath)
		}
		return models.ToolExecutionResult{}, fmt.Errorf("write temp failed: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if hadBackup {
			_ = os.Rename(backupPath, path)
		}
		_ = os.Remove(tmpPath)
		return models.ToolExecutionResult{}, fmt.Errorf("commit failed: %w; restored from backup", err)
	}

	if hadBackup {
		_ = os.Remove(backupPath)
	}

	details := map[string]any{"path": path}
	if oldContent != "" {
		details["old_content"] = oldContent
	}
	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Wrote %d characters to %s", len(content), path)},
		},
		Details: details,
	}, nil
}

// maxWriteDiffOldSize caps the previous file content shipped in the write
// result's details (key "old_content", consumed by the TUI diff preview) so
// tool events stay small.
const maxWriteDiffOldSize = 256 * 1024

var _ tools.Executable = (*Write)(nil)
