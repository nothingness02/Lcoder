// Package agentsetup holds the agent-construction pieces shared between the
// real binary (cmd/lcoder) and the integration tests, so both wire the agent
// from the same code instead of drifting copies. Keeping the system prompt and
// context-manager construction here means a change to either takes effect in
// production and in tests at once.
package agentsetup

import (
	"os"

	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
)

// BuildSystemPrompt assembles the shared base system prompt: a fixed persona
// and operating contract. Project context and activated skills are injected as
// separate context-manager blocks (project_docs / skills) so they are not
// duplicated in the system block.
func BuildSystemPrompt() string {
	// var b strings.Builder
	// b.WriteString("You are Lcoder, an expert software engineering agent.\n\n")
	// b.WriteString("Operating guidelines:\n")
	// b.WriteString("- Ground every claim in tool output. Never answer about file contents, repository state, or command results from memory or assumption — read the file or run the command first, then answer from what the tool actually returned.\n")
	// b.WriteString("- Prefer parallel tool calls when the operations are independent.\n")
	// b.WriteString("- Keep working across turns until the task is genuinely complete. You see each tool's result before choosing the next step, so verify rather than guess.\n")
	// b.WriteString("- Signal completion by replying with a final message that makes NO tool calls: a concise summary of what you did and the outcome. Do not stop early with a plain-text answer while work remains, and do not keep calling tools once the task is done.")
	// return b.String()
	// 开发环境用txt来快速构建和查看提示词的更改带来的效果的影响
	candidates := []string{
		"configs/agents/system.txt",              // 从项目根目录运行
		"../../configs/agents/system.txt",        // 从 pkg/agentsetup 测试
		paths.LCoderHome("agents", "system.txt"), // 用户全局覆盖
	}
	for _, path := range candidates {
		if content, err := os.ReadFile(path); err == nil {
			return string(content)
		}
	}
	return ""
}

// NewContextManager builds the token-budgeted context manager with the system,
// project-docs, skills, and recent blocks. A summarizer is always attached so
// that checkpoint restore has a wired manager and compaction can run when the
// budget policy asks for it.
func NewContextManager(cfg config.Config, budget config.TokenBudget, llmClient *llm.Client, contextText, skillsBlock string, activeMessages []models.AgentMessage, memStore *memory.Store) *contextmgr.Manager {
	opts := []contextmgr.Option{
		contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(cfg.Context.MinRecent)),
		contextmgr.WithMinRecent(cfg.Context.MinRecent),
		contextmgr.WithKeepRecentTokens(cfg.Context.KeepRecentTokens),
		contextmgr.WithCacheHintPolicy(contextmgr.ParseCacheHintPolicy(cfg.Context.CacheHintPolicy)),
		contextmgr.WithSummarizer(contextmgr.SummarizeFunc(compaction.NewCircuitBreaker(0).Wrap(compaction.NewLLMSummarizer(llmClient, models.ModelRef{Provider: cfg.Provider, ID: cfg.Model})))),
	}

	mgr := contextmgr.NewManager(contextmgr.TokenBudget{
		MaxTotal:         budget.MaxTotal,
		TargetTotal:      budget.TargetTotal,
		ReserveOutput:    budget.ReserveOutput,
		MaxOutput:        budget.MaxOutput,
		CompactThreshold: budget.CompactThreshold,
		DropThreshold:    budget.DropThreshold,
		StaticRatio:      cfg.Context.StaticRatio,
	}, opts...)

	systemText := BuildSystemPrompt()
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: systemText})))

	if contextText != "" {
		mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockProjectDocs, "project_docs", contextmgr.StabilityStable, 80, contextmgr.CacheHintBreakpoint,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: contextText})))
	}

	if skillsBlock != "" {
		mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockSkills, "skills", contextmgr.StabilityStable, 90, contextmgr.CacheHintBreakpoint,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: skillsBlock})))
	}

	if memStore != nil && cfg.Memory.Enabled {
		if userText, err := memStore.UserText(); err == nil && userText != "" {
			mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockUserProfile, "user_profile", contextmgr.StabilityStable, 70, contextmgr.CacheHintBreakpoint,
				models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: userText})))
		}
		if !cfg.Memory.DynamicRecall {
			if memoryText, err := memStore.MemoryText(); err == nil && memoryText != "" {
				mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockMemory, "memory", contextmgr.StabilityStable, 75, contextmgr.CacheHintBreakpoint,
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: memoryText})))
			}
		}
	}

	if len(activeMessages) > 0 {
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, activeMessages...))
	}

	return mgr
}
