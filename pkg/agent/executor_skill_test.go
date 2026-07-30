package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// newSkillFilterExecutor builds an executor with two real skills on disk:
// "sec-review" (allowed_tools: [read]) and "freeform" (unrestricted). read and
// ls are fake registered tools so no filesystem access happens.
func newSkillFilterExecutor(t *testing.T) *executor {
	t.Helper()
	dir := t.TempDir()
	writeSkillFile(t, dir, "sec-review", `---
name: sec-review
description: Review code
allowed_tools:
  - read
---
Review body.
`)
	writeSkillFile(t, dir, "freeform", `---
name: freeform
description: Unrestricted skill
---
Freeform body.
`)
	catalog := skills.Discover([]skills.Source{{Scope: skills.ScopeUser, Dir: dir}})

	r := tools.NewRegistry(".")
	r.Register(skills.UseSkillToolName, builtin.NewUseSkill(".", catalog))
	r.Register("read", fakeTool{fullDef("read", "Read file.")})
	r.Register("ls", fakeTool{fullDef("ls", "List files.")})

	cfg := Config{}
	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}
}

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callTool(e *executor, name string, args map[string]any) models.ToolResultContent {
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, []models.ToolCallContent{
		{ID: "call_" + name, Name: name, Arguments: args},
	}, models.ExecutionParallel)
	return results[0].Content[0].(models.ToolResultContent)
}

func TestSkillFilterRestrictsAfterActivation(t *testing.T) {
	e := newSkillFilterExecutor(t)

	// Activate the restricted skill: body arrives as the tool result.
	res := callTool(e, skills.UseSkillToolName, map[string]any{"skill_name": "sec-review"})
	if res.IsError {
		t.Fatalf("activation failed: %q", res.Text())
	}
	if !strings.Contains(res.Text(), "Review body.") {
		t.Fatalf("expected skill body in result, got %q", res.Text())
	}

	// ls is not in allowed_tools: rejected at execution time.
	res = callTool(e, "ls", map[string]any{"path": "."})
	if !res.IsError {
		t.Fatal("expected ls to be rejected by the active skill")
	}
	if !strings.Contains(res.Text(), "restricted by the active skill") {
		t.Fatalf("unexpected rejection text: %q", res.Text())
	}

	// read is allowed and goes through.
	if res = callTool(e, "read", map[string]any{"path": "."}); res.IsError {
		t.Fatalf("expected read to be allowed, got %q", res.Text())
	}

	// use_skill itself is exempt, so the model can switch skills.
	if res = callTool(e, skills.UseSkillToolName, map[string]any{"skill_name": "freeform"}); res.IsError {
		t.Fatalf("use_skill must be exempt from the filter: %q", res.Text())
	}
}

func TestSkillFilterLiftedByUnrestrictedSkill(t *testing.T) {
	e := newSkillFilterExecutor(t)

	callTool(e, skills.UseSkillToolName, map[string]any{"skill_name": "sec-review"})
	if res := callTool(e, "ls", map[string]any{"path": "."}); !res.IsError {
		t.Fatal("expected ls to be restricted while sec-review is active")
	}

	// Activating a skill without allowed_tools lifts the restriction.
	callTool(e, skills.UseSkillToolName, map[string]any{"skill_name": "freeform"})
	if res := callTool(e, "ls", map[string]any{"path": "."}); res.IsError {
		t.Fatalf("expected ls to work after lifting the restriction, got %q", res.Text())
	}
}

func TestSkillFilterUnaffectedByFailedActivation(t *testing.T) {
	e := newSkillFilterExecutor(t)

	// A failed activation (unknown skill) must not install a filter.
	if res := callTool(e, skills.UseSkillToolName, map[string]any{"skill_name": "nope"}); !res.IsError {
		t.Fatal("expected error for unknown skill")
	}
	if res := callTool(e, "ls", map[string]any{"path": "."}); res.IsError {
		t.Fatalf("failed activation must not restrict tools, got %q", res.Text())
	}
}
