package agentsetup

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Stability is what the cache layer reads to decide the block order and which
// blocks may be dropped under pressure: static/stable blocks form the cached
// prefix, dynamic blocks are the churning tail. Mislabelling a block does not
// fail any request — it silently degrades cache hit rate or drops the wrong
// content under pressure — so the contract is pinned here against the real
// producer rather than left to reviewer attention.
//
// Priority is asserted alongside it because enforceStaticRatio drops
// static/stable blocks in ascending priority order: the system prompt must
// outrank project docs and skills so it is dropped last.
func TestNewContextManager_BlockStabilityContract(t *testing.T) {
	mgr := NewContextManager(
		config.Config{},
		config.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
		"", nil,
		"PROJECT DOCS",
		"SKILLS",
		[]models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
		nil,
	)

	for _, tc := range []struct {
		kind      contextmgr.BlockKind
		name      string
		stability contextmgr.Stability
		priority  int
	}{
		{contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100},
		{contextmgr.BlockSkills, "skills", contextmgr.StabilityStable, 90},
		{contextmgr.BlockProjectDocs, "project_docs", contextmgr.StabilityStable, 80},
		{contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			b, ok := mgr.GetBlock(tc.kind, tc.name)
			if !ok {
				t.Fatalf("producer did not write block %s/%s", tc.kind, tc.name)
			}
			if b.Stability != tc.stability {
				t.Errorf("stability: got %q, want %q", b.Stability, tc.stability)
			}
			if b.Priority != tc.priority {
				t.Errorf("priority: got %d, want %d", b.Priority, tc.priority)
			}
		})
	}
}

// The point of the stability labels: assembly order must run static -> stable
// -> dynamic, because a provider prefix cache only helps while the prefix is
// byte-stable. A dynamic block ordered ahead of a stable one would invalidate
// everything behind it on the turn it changes.
func TestNewContextManager_StabilityOrdering(t *testing.T) {
	mgr := NewContextManager(
		config.Config{},
		config.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
		"", nil, "PROJECT DOCS", "SKILLS",
		[]models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
		nil,
	)

	rank := map[contextmgr.Stability]int{
		contextmgr.StabilityStatic:  0,
		contextmgr.StabilityStable:  1,
		contextmgr.StabilityDynamic: 2,
	}
	prev := -1
	for _, b := range mgr.Blocks() {
		r, ok := rank[b.Stability]
		if !ok {
			t.Fatalf("block %s/%s has unknown stability %q", b.Kind, b.Name, b.Stability)
		}
		if r < prev {
			t.Fatalf("block %s/%s (%s) is ordered after a less stable block: assembly order must be static -> stable -> dynamic",
				b.Kind, b.Name, b.Stability)
		}
		prev = r
	}
}
