package tui

import "testing"

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
