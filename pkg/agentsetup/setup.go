// Package agentsetup holds the agent-construction pieces shared between the
// real binary (cmd/lcoder) and the integration tests, so both wire the agent
// from the same code instead of drifting copies. Keeping the system prompt and
// context-manager construction here means a change to either takes effect in
// production and in tests at once.
package agentsetup

import (
	"os"
	"strings"
	"sync"
	"text/template"

	"github.com/lcoder/lcoder"
	"github.com/lcoder/lcoder/internal/paths"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
)

// TemplateContext provides runtime values for template rendering in system.md.
type TemplateContext struct {
	CWD   string
	OS    string
	Shell string
	Now   string
}

// BuildSystemPrompt assembles the shared base system prompt from system.md,
// rendering template variables ({{ .CWD }}, {{ .OS }}, etc.) with the given
// context. Project context and activated skills are injected as separate
// context-manager blocks (project_docs / skills) so they are not duplicated.
func BuildSystemPrompt(ctx TemplateContext) string {
	// Prompts live in markdown files, not code. Precedence: user override
	// (~/.lcoder/modes/system.md) -> dev checkout (configs/modes/system.md,
	// so prompt edits take effect without a rebuild) -> embedded default
	// (always present, even for single-file installs).
	candidates := []string{
		paths.LCoderHome("modes", "system.md"),
		"configs/modes/system.md",
		"../../configs/modes/system.md", // from pkg/agentsetup tests
	}
	var tmpl string
	for _, path := range candidates {
		if content, err := os.ReadFile(path); err == nil {
			tmpl = string(content)
			break
		}
	}
	if tmpl == "" {
		tmpl = lcoder.SystemPromptMD
	}
	return renderTemplate(tmpl, ctx)
}

func renderTemplate(tmpl string, data any) string {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return tmpl
	}
	return buf.String()
}

// NewContextManager builds the token-budgeted context manager with the system,
// project-docs, skills, and recent blocks. A summarizer is always attached so
// that checkpoint restore has a wired manager and compaction can run when the
// budget policy asks for it.
// CompactionRecorder is the slice of the session store that a committed fold has
// to reach. Declared here as an interface so agentsetup does not depend on
// pkg/session, and so tests can record folds without a session on disk.
type CompactionRecorder interface {
	// AppendCompactionEntry records the fold itself: the summary text and the id
	// of the first kept message, from which the compacted view is rebuilt.
	AppendCompactionEntry(summary, firstKeptEntryID string, tokensBefore int) error
	// AppendMissing mirrors messages not yet on disk, deduped by id.
	AppendMissing(msgs []models.AgentMessage) error
}

// ActiveSession holds the session a committed fold should be written to. The TUI
// swaps sessions mid-run (/sessions, /new), so the sink reads through this rather
// than capturing whichever session was open when the manager was built.
//
// Set is called from the UI goroutine and the sink reads it from the agent loop,
// so both go through the mutex.
type ActiveSession struct {
	mu  sync.Mutex
	rec CompactionRecorder
}

// NewActiveSession returns a holder seeded with the starting session.
func NewActiveSession(rec CompactionRecorder) *ActiveSession {
	return &ActiveSession{rec: rec}
}

// Set replaces the session that subsequent folds are recorded to.
func (a *ActiveSession) Set(rec CompactionRecorder) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rec = rec
}

// Get returns the session folds are currently recorded to.
func (a *ActiveSession) Get() CompactionRecorder {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rec
}

// SessionCompactionSink returns the sink that persists a committed fold. It runs
// inside foldOlder, so the durable record cannot drift from the in-memory fold
// the way a separate event subscription could.
//
// The recorder is resolved per fold rather than captured: the TUI can switch the
// active session mid-run (/sessions, /new), and a sink bound to the session that
// happened to be open at startup would write the fold to the wrong file.
//
// Degraded folds are recorded too: their summary is the explicit
// summary-unavailable notice, and the older span really was dropped from the live
// context, so without an entry the session's compacted view would still claim
// those messages are active and a resume would replay them.
func SessionCompactionSink(active func() CompactionRecorder) contextmgr.CompactionSink {
	if active == nil {
		return nil
	}
	return func(res contextmgr.FoldResult, live []models.AgentMessage) error {
		if res.Summary == "" {
			return nil
		}
		rec := active()
		if rec == nil {
			return nil
		}
		if err := rec.AppendCompactionEntry(res.Summary, res.FirstKeptID, res.TokensBefore); err != nil {
			return err
		}
		// With the entry on disk, AppendMissing skips the runtime summary message
		// and appends only the not-yet-persisted kept tail, so a crash before the
		// next turn boundary cannot lose it.
		return rec.AppendMissing(live)
	}
}

func NewContextManager(cfg config.Config, budget config.TokenBudget, thinking string, llmClient *llm.Client, contextText, skillsBlock string, activeMessages []models.AgentMessage, sink contextmgr.CompactionSink, tmplCtx TemplateContext) *contextmgr.Manager {
	opts := []contextmgr.Option{
		contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(cfg.Context.MinRecent)),
		contextmgr.WithMinRecent(cfg.Context.MinRecent),
		contextmgr.WithKeepRecentTokens(cfg.Context.KeepRecentTokens),
		contextmgr.WithCacheHintPolicy(contextmgr.ParseCacheHintPolicy(cfg.Context.CacheHintPolicy)),
		contextmgr.WithSummarizer(contextmgr.SummarizeFunc(compaction.NewCircuitBreaker(0).Wrap(compaction.NewLLMSummarizer(llmClient, models.ModelRef{Provider: cfg.Provider, ID: cfg.Model})))),
	}
	if thinking != "" {
		opts = append(opts, contextmgr.WithThinking(thinking))
	}
	if sink != nil {
		opts = append(opts, contextmgr.WithCompactionSink(sink))
	}
	if cfg.Context.MicroCompact {
		opts = append(opts, contextmgr.WithMicroCompact(contextmgr.MicroCompactConfig{
			Enabled:        cfg.Context.MicroCompact,
			KeepRecent:     cfg.Context.MicroCompactKeepRecent,
			MinChars:       cfg.Context.MicroCompactMinChars,
			CacheMissedMs:  cfg.Context.MicroCompactCacheMissedMs,
			MinUsageRatio:  cfg.Context.MicroCompactMinUsageRatio,
		}))
	}

	mgr := contextmgr.NewManager(contextmgr.TokenBudget{
		MaxTotal:         budget.MaxTotal,
		TargetTotal:      budget.TargetTotal,
		ReserveOutput:    budget.ReserveOutput,
		MaxOutput:        budget.MaxOutput,
		DropThreshold:    budget.DropThreshold,
		StaticRatio:      cfg.Context.StaticRatio,
	}, opts...)

	systemText := BuildSystemPrompt(tmplCtx)
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: systemText})))

	if contextText != "" {
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockProjectDocs, "project_docs", contextmgr.StabilityStable, 80,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: contextText})))
	}

	if skillsBlock != "" {
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSkills, "skills", contextmgr.StabilityStable, 90,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: skillsBlock})))
	}

	if len(activeMessages) > 0 {
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, activeMessages...))
	}

	return mgr
}
