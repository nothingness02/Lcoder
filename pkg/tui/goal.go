package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agentapi"
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

// handleGoalCommand implements the /goal command family. The pursuit loop
// itself lives in the host's goal driver (started by StartGoal/ResumeGoal);
// the TUI only issues the commands and renders state.
func handleGoalCommand(m *Model, args string) tea.Cmd {
	sub, objective, turns, tokens := parseGoalArgs(args)
	switch sub {
	case "start":
		// StartGoal launches the host goal driver, whose first run uses the
		// objective as its prompt (kimi-code's /goal semantics); continuations
		// are driven by the host. The TUI renders the objective as a user bar
		// and enters the processing UI; the run's AgentStart/AgentEnd events
		// take it from there.
		m.agent.StartGoal(objective, turns, tokens)
		m.addUser(objective)
		m.viewport.GotoBottom()
		m.rebuildViewport()
		m.state = stateProcessing
		m.input.SetProcessing(true)
		// Same cleanup as startPrompt: stale run errors, transient notices,
		// and block focus must not leak into the new pursuit.
		m.errMsg = ""
		m.notice = ""
		m.focusedBlockIndex = -1
		m.showTextPanel("goal", styleSuccess().Render("goal started: "+objective))
		return spinnerTick()
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
	case "cancel":
		m.agent.CancelGoal()
		m.showTextPanel("goal", styleDim().Render("goal cleared"))
	}
	return nil
}

// formatGoalStatus renders the goal record for the status panel.
func formatGoalStatus(g *agentapi.GoalState) string {
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
