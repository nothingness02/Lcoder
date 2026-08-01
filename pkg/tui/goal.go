package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
)

// parseGoalArgs parses /goal arguments into a subcommand and, for start, the
// objective and budgets. Bare /goal is equivalent to /goal status.
func parseGoalArgs(args string) (sub, objective string, turns, tokens int) {
	fields := strings.Fields(args)
	for _, f := range fields {
		if v, ok := strings.CutPrefix(f, "--turns="); ok {
			turns, _ = strconv.Atoi(v)
			continue
		}
		if v, ok := strings.CutPrefix(f, "--tokens="); ok {
			tokens, _ = strconv.Atoi(v)
			continue
		}
		switch f {
		case "status", "pause", "resume", "cancel":
			if objective == "" {
				return f, "", 0, 0
			}
		}
		objective = strings.TrimSpace(objective + " " + f)
	}
	if objective == "" {
		return "status", "", 0, 0
	}
	return "start", objective, turns, tokens
}

// handleGoalCommand implements the /goal command family.
func handleGoalCommand(m *Model, args string) tea.Cmd {
	sub, objective, turns, tokens := parseGoalArgs(args)
	switch sub {
	case "start":
		m.agent.StartGoal(objective, turns, tokens)
		m.showTextPanel("goal", styleSuccess().Render("goal started: "+objective))
		// 立即以 objective 开跑第一个 run(kimi-code 的 /goal 语义);之后的
		// continuation 由 onAgentDone 接线(见 keys.go)。
		return m.startPrompt(objective)
	case "status":
		m.showTextPanel("goal", formatGoalStatus(m.agent.Goal()))
	case "pause":
		m.agent.PauseGoal("paused by user")
		m.showTextPanel("goal", styleDim().Render("goal paused"))
	case "resume":
		if g := m.agent.Goal(); g == nil {
			m.showTextPanel("goal", styleError().Render("no goal to resume"))
			return nil
		}
		m.agent.ResumeGoal()
		m.showTextPanel("goal", styleSuccess().Render("goal resumed"))
		if cmd := m.continueGoalIfActive(); cmd != nil {
			return cmd
		}
	case "cancel":
		m.agent.CancelGoal()
		m.showTextPanel("goal", styleDim().Render("goal cleared"))
	}
	return nil
}

// formatGoalStatus renders the goal record for the status panel.
func formatGoalStatus(g *agent.GoalState) string {
	if g == nil {
		return styleDim().Render("no active goal")
	}
	budget := fmt.Sprintf("turns %d", g.TurnsUsed)
	if g.TurnBudget > 0 {
		budget = fmt.Sprintf("turns %d/%d", g.TurnsUsed, g.TurnBudget)
	}
	tokens := fmt.Sprintf("tokens %d", g.TokensUsed)
	if g.TokenBudget > 0 {
		tokens = fmt.Sprintf("tokens %d/%d", g.TokensUsed, g.TokenBudget)
	}
	out := fmt.Sprintf("goal [%s]: %s\n%s · %s", g.Status, g.Objective, budget, tokens)
	if g.BlockReason != "" {
		out += "\nreason: " + g.BlockReason
	}
	return out
}

// continueGoalIfActive submits the next continuation prompt when a goal is
// still active after a run (or a resume). Returns nil when the pursuit ends.
func (m *Model) continueGoalIfActive() tea.Cmd {
	g := m.agent.Goal()
	prompt, done := agent.NextGoalAction(g, m.agent.LastEndReason())
	if done {
		if g != nil && g.Status == agent.GoalActive {
			m.agent.PauseGoal("goal driver stopped")
		}
		return nil
	}
	m.state = stateProcessing
	m.input.SetProcessing(true)
	m.runner.SubmitPrompt(prompt)
	return waitForRunnerResultCmd(m.runner.Results())
}
