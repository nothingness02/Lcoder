package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// ContextSnapshotRecorder writes full context snapshots to independent Markdown
// files. It is disabled by default and can be toggled at runtime to avoid
// production overhead.
type ContextSnapshotRecorder struct {
	mu        sync.Mutex
	enabled   bool
	sessionID string
	outputDir string
	maxMsgs   int
}

// NewContextSnapshotRecorder creates a recorder from configuration.
func NewContextSnapshotRecorder(sessionID string, cfg config.ContextSnapshotsConfig) *ContextSnapshotRecorder {
	outputDir := cfg.OutputDir
	if outputDir == "" {
		outputDir = paths.LCoderHome("context-snapshots", sessionID)
	}
	return &ContextSnapshotRecorder{
		enabled:   cfg.Enabled,
		sessionID: sessionID,
		outputDir: outputDir,
		maxMsgs:   cfg.MaxMessagesPerBlock,
	}
}

// SetEnabled toggles snapshot collection at runtime.
func (r *ContextSnapshotRecorder) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = enabled
}

// Record renders the manager state as Markdown and writes it to a file when
// enabled.
func (r *ContextSnapshotRecorder) Record(state *contextmgr.ManagerState, phase string, turn int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled {
		return nil
	}

	if err := fsutil.EnsurePrivateDir(r.outputDir); err != nil {
		return err
	}

	path := filepath.Join(r.outputDir, fmt.Sprintf("context-turn-%d-%s.md", turn, phase))
	return fsutil.WritePrivateFile(path, []byte(r.render(state, phase, turn)))
}

func (r *ContextSnapshotRecorder) render(state *contextmgr.ManagerState, phase string, turn int) string {
	var b bytes.Buffer
	b.WriteString("# Context Snapshot\n\n")
	b.WriteString(fmt.Sprintf("- Session: %s\n", r.sessionID))
	b.WriteString(fmt.Sprintf("- Turn: %d\n", turn))
	b.WriteString(fmt.Sprintf("- Phase: %s\n", phase))
	b.WriteString(fmt.Sprintf("- Generated: %s\n\n", time.Now().Format(time.RFC3339)))

	b.WriteString("## Budget\n\n")
	b.WriteString(fmt.Sprintf("- MaxTotal: %d\n", state.Budget.MaxTotal))
	b.WriteString(fmt.Sprintf("- TargetTotal: %d\n", state.Budget.TargetTotal))
	b.WriteString(fmt.Sprintf("- ReserveOutput: %d\n", state.Budget.ReserveOutput))
	b.WriteString(fmt.Sprintf("- MaxOutput: %d\n", state.Budget.MaxOutput))
	b.WriteString(fmt.Sprintf("- DropThreshold: %v\n\n", state.Budget.DropThreshold))

	b.WriteString(fmt.Sprintf("## Blocks (%d)\n\n", len(state.Blocks)))
	for _, block := range state.Blocks {
		b.WriteString(fmt.Sprintf("### Block: %s\n\n", block.Name))
		b.WriteString(fmt.Sprintf("- Kind: %s\n", block.Kind))
		b.WriteString(fmt.Sprintf("- Stability: %s\n", block.Stability))
		b.WriteString(fmt.Sprintf("- Priority: %d\n", block.Priority))
		b.WriteString(fmt.Sprintf("- CacheHint: %s\n", block.CacheHint))
		b.WriteString(fmt.Sprintf("- Messages: %d\n\n", len(block.Messages)))

		if len(block.Messages) > 0 {
			b.WriteString("#### Messages\n\n")
			limit := len(block.Messages)
			if r.maxMsgs > 0 && r.maxMsgs < limit {
				limit = r.maxMsgs
			}
			for i := 0; i < limit; i++ {
				msg := block.Messages[i]
				b.WriteString(fmt.Sprintf("**%s**:\n\n%s\n\n", msg.Role, r.renderMessage(msg)))
			}
			if limit < len(block.Messages) {
				b.WriteString(fmt.Sprintf("_... and %d more messages_\n\n", len(block.Messages)-limit))
			}
		}
	}

	if state.LastUsage != nil {
		b.WriteString("## Last Usage\n\n")
		b.WriteString(fmt.Sprintf("- InputTokens: %d\n", state.LastUsage.InputTokens))
		b.WriteString(fmt.Sprintf("- CacheReadTokens: %d\n", state.LastUsage.CacheReadTokens))
		b.WriteString(fmt.Sprintf("- CacheCreationTokens: %d\n", state.LastUsage.CacheCreationTokens))
		b.WriteString(fmt.Sprintf("- PromptTokens: %d\n", state.LastUsage.PromptTokens()))
	}

	return b.String()
}

func (r *ContextSnapshotRecorder) renderMessage(msg models.AgentMessage) string {
	if len(msg.Content) == 0 {
		return "(empty message)"
	}
	return r.renderContentParts(msg.Content, 0)
}

func (r *ContextSnapshotRecorder) renderContentParts(parts []models.ContentPart, indent int) string {
	if len(parts) == 0 {
		return "(no content)"
	}
	prefix := ""
	for i := 0; i < indent; i++ {
		prefix += "> "
	}
	var out bytes.Buffer
	for _, part := range parts {
		out.WriteString(prefix)
		switch p := part.(type) {
		case models.TextContent:
			out.WriteString(p.Text)
		case models.ThinkingContent:
			out.WriteString("**[thinking]**\n")
			if prefix != "" {
				out.WriteString(prefix)
			}
			out.WriteString(p.Text)
		case models.ToolCallContent:
			args, _ := json.Marshal(p.Arguments)
			out.WriteString(fmt.Sprintf("**[tool_call: %s]** id=%s args=%s", p.Name, p.ID, string(args)))
		case models.ToolResultContent:
			out.WriteString(fmt.Sprintf("**[tool_result: %s/%s]**", p.ToolCallID, p.Name))
			if p.IsError {
				out.WriteString(" (error)")
			}
			if len(p.Details) > 0 {
				details, _ := json.Marshal(p.Details)
				out.WriteString(fmt.Sprintf(" details=%s", string(details)))
			}
			out.WriteString("\n")
			out.WriteString(r.renderContentParts(p.Content, indent+1))
		case models.ImageContent:
			out.WriteString(fmt.Sprintf("**[image/%s]**", p.MimeType))
		default:
			out.WriteString(fmt.Sprintf("**[unknown content part: %T]**", part))
		}
		out.WriteString("\n\n")
	}
	return out.String()
}
