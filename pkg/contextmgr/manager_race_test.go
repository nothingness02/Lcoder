package contextmgr

import (
	"sync"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// The TUI reads Stats/AllMessages/Budget on its render loop while the agent
// goroutine appends messages, replaces blocks, and records usage. Run with
// -race: before the manager-wide locking this flagged the unsynchronized
// m.blocks and m.lastUsage accesses.
func TestManagerConcurrentReadWrite(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 80000, ReserveOutput: 4096})
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "seed"}))

	const workers = 4
	const iters = 200
	var wg sync.WaitGroup

	// Agent-goroutine style writers.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "msg"}))
				m.SetBlock(NewBlock(BlockProjectDocs, "docs", StabilityStable, 50,
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "docs"})))
				m.RecordRealUsage(models.LLMUsage{PromptTokens: 100 + i})
				m.SetBudget(TokenBudget{MaxTotal: 100000 + i, TargetTotal: 80000, ReserveOutput: 4096})
				m.RemoveBlock(BlockProjectDocs, "docs")
			}
		}(w)
	}

	// UI-goroutine style readers.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = m.Stats()
				_ = m.AllMessages()
				_ = m.SystemPrompt()
				_ = m.Budget()
				_, _ = m.RealPromptTokens()
				_ = m.MicroCompactStatus()
				_ = m.Blocks()
			}
		}()
	}
	wg.Wait()
}

// BuildTurnRequest runs on the agent goroutine while the TUI toggles skill
// blocks and reads stats; -race catches torn reads across the whole build.
func TestManagerConcurrentBuildAndToggle(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 80000, ReserveOutput: 4096})
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "seed"}))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := m.BuildTurnRequest(models.ModelRef{Provider: "test", ID: "x"}, nil); err != nil {
				t.Errorf("BuildTurnRequest: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.SetBlock(NewBlock(BlockSkills, "skills", StabilityStable, 90,
				models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "skills"})))
			m.RemoveBlock(BlockSkills, "skills")
			_ = m.Stats()
		}
	}()
	wg.Wait()
}
