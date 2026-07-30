package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Edit performs exact-text replacements in a file.
type Edit struct {
	cwd string
}

// NewEdit creates an edit tool.
func NewEdit(cwd string) tools.Executable {
	return &Edit{cwd: cwd}
}

func (e *Edit) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "edit",
		Description: "Edit a single file using exact text replacement. Read the file first and copy oldText exactly from the read output; " +
			"do not edit from memory. Each oldText must match a unique region unless replaceAll is set. " +
			"Line endings: pure CRLF files are matched in the LF view shown by read and written back as CRLF; " +
			"files with mixed line endings are matched byte-exactly, with carriage returns shown by read as literal \\r.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to edit",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "One or more targeted replacements, applied in order",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"oldText": map[string]any{
								"type":        "string",
								"description": "Exact text to replace, copied from the read output",
							},
							"newText": map[string]any{
								"type":        "string",
								"description": "Replacement text",
							},
							"replaceAll": map[string]any{
								"type":        "boolean",
								"description": "Replace every occurrence of oldText instead of requiring a unique match",
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
	oldText    string
	newText    string
	replaceAll bool
}

func parseEdits(args map[string]any) ([]editOp, error) {
	editsRaw, ok := args["edits"].([]any)
	if !ok || len(editsRaw) == 0 {
		return nil, fmt.Errorf("missing edits")
	}
	out := make([]editOp, 0, len(editsRaw))
	for i, raw := range editsRaw {
		edit, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d]: invalid edit entry", i)
		}
		oldText, ok := edit["oldText"].(string)
		if !ok {
			return nil, fmt.Errorf("edits[%d]: missing oldText", i)
		}
		if oldText == "" {
			return nil, fmt.Errorf("edits[%d]: oldText must not be empty", i)
		}
		newText, ok := edit["newText"].(string)
		if !ok {
			return nil, fmt.Errorf("edits[%d]: missing newText", i)
		}
		if oldText == newText {
			return nil, fmt.Errorf("edits[%d]: oldText and newText are identical; no changes to make", i)
		}
		replaceAll, _ := edit["replaceAll"].(bool)
		out = append(out, editOp{oldText: oldText, newText: newText, replaceAll: replaceAll})
	}
	return out, nil
}

// applyEdits applies each edit in order against the model-view text. Every
// failure message tells the model how to recover.
func applyEdits(text string, edits []editOp) (string, error) {
	for i, e := range edits {
		count := strings.Count(text, e.oldText)
		switch {
		case count == 0:
			return "", fmt.Errorf(
				"edits[%d]: oldText not found; the file contents may be out of date — read the file again and copy oldText exactly from the read output", i)
		case count > 1 && !e.replaceAll:
			return "", fmt.Errorf(
				"edits[%d]: oldText is not unique (found %d occurrences); include more surrounding context to make it unique, or set replaceAll=true to replace every occurrence", i, count)
		}
		if e.replaceAll {
			text = strings.ReplaceAll(text, e.oldText, e.newText)
		} else {
			text = strings.Replace(text, e.oldText, e.newText, 1)
		}
	}
	return text, nil
}

// DeclareAccesses implements tools.AccessDeclarer: an edit reads and writes its file.
func (e *Edit) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return []tools.ToolAccess{{Op: tools.OpAll}}
	}
	return []tools.ToolAccess{{Op: tools.OpReadWrite, Path: resolveInCwd(e.cwd, path)}}
}

func (e *Edit) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	path = resolveInCwd(e.cwd, path)

	edits, err := parseEdits(args)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	// os.Stat follows symlinks but os.Rename would replace the link itself:
	// resolve the real path first so editing a symlink edits its target and
	// the link survives.
	if lst, err := os.Lstat(path); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if info.IsDir() {
		return models.ToolExecutionResult{}, fmt.Errorf("%s is not a file", path)
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if !utf8.Valid(original) {
		return models.ToolExecutionResult{}, fmt.Errorf("%s is not a UTF-8 text file; edit only supports UTF-8", path)
	}

	// Stage 1: dry-run in memory, in the model view shared with the read
	// tool (pure CRLF files match as LF; mixed files match raw bytes).
	style := detectLineEndingStyle(string(original))
	view := toModelTextView(string(original), style)
	newView, err := applyEdits(view, edits)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("%s: %w", path, err)
	}
	committed := materializeModelText(newView, style)

	// Stage 2: commit with backup + atomic rename, preserving the original
	// file's permission bits. tmp/backup names carry the call id so parallel
	// edits to the same file cannot clobber each other's staging files.
	backupPath := path + backupSuffix + "." + callID
	if err := os.WriteFile(backupPath, original, 0o600); err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("backup failed: %w", err)
	}

	tmpPath := path + tmpSuffix + "." + callID
	if err := os.WriteFile(tmpPath, []byte(committed), info.Mode().Perm()); err != nil {
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

var _ tools.Executable = (*Edit)(nil)
