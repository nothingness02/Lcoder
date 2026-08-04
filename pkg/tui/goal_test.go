package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestParseGoalCommand(t *testing.T) {
	tests := []struct {
		args               string
		wantSub            string
		wantObjective      string
		wantTurns, wantTok int
	}{
		{"fix the failing test", "start", "fix the failing test", 0, 0},
		{"--turns=20 --tokens=50000 fix the test", "start", "fix the test", 20, 50000},
		{"status", "status", "", 0, 0},
		{"pause", "pause", "", 0, 0},
		{"resume", "resume", "", 0, 0},
		{"cancel", "cancel", "", 0, 0},
		{"", "status", "", 0, 0}, // 裸 /goal 等价 status
	}
	for _, tt := range tests {
		sub, objective, turns, tok := parseGoalArgs(tt.args)
		if sub != tt.wantSub || objective != tt.wantObjective || turns != tt.wantTurns || tok != tt.wantTok {
			t.Errorf("parseGoalArgs(%q) = (%q, %q, %d, %d), want (%q, %q, %d, %d)",
				tt.args, sub, objective, turns, tok, tt.wantSub, tt.wantObjective, tt.wantTurns, tt.wantTok)
		}
	}
}

// /goal <objective> 建立 goal 后必须立即把 objective 作为第一条 prompt 开始追求
// (对齐 kimi-code:/goal 即开始追求,而不是等用户再输入一条)。续跑由 host 的
// goal driver 承担;TUI 侧同步可观测的判据:objective 作为 user 块进入
// transcript 且进入 processing。
func TestGoalStartSubmitsObjectivePrompt(t *testing.T) {
	m := newTestCoreModel(&fakeAgent{})
	m.state = stateInput
	m.dispatchSlash("/goal fix the failing test")

	var sawObjective bool
	for _, b := range m.blocks {
		if b.kind == components.BlockUser && strings.Contains(b.raw, "fix the failing test") {
			sawObjective = true
		}
	}
	if !sawObjective {
		t.Fatal("/goal must immediately submit the objective as the first prompt")
	}
	if m.state != stateProcessing {
		t.Fatalf("model must enter processing state, got %v", m.state)
	}
	if g := m.agent.Goal(); g == nil || g.Status != agentapi.GoalActive {
		t.Fatal("goal must be active")
	}
}
