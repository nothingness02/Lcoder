package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/permissions"
)

// confirmResult is returned to the blocked tool call goroutine.
type confirmResult struct {
	allow bool
	scope agent.ConfirmScope
	err   error
}

// confirmRequest carries a pending confirmation into the Bubble Tea loop.
type confirmRequest struct {
	info agent.ToolCallInfo
	resp chan confirmResult
}

// confirmRequestMsg asks the UI to show a permission prompt.
type confirmRequestMsg struct {
	req confirmRequest
}

// confirmResponseMsg carries the user's decision back into the loop.
type confirmResponseMsg struct {
	allow bool
	scope agent.ConfirmScope
}

// programSender matches the part of *tea.Program that tuiConfirm needs.
type programSender interface {
	Send(tea.Msg)
}

// tuiConfirm implements agent.UserConfirmation by delegating to the Bubble Tea
// event loop. It blocks the tool-call goroutine until the user responds.
type tuiConfirm struct {
	program programSender
}

func (c *tuiConfirm) Confirm(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
	res, err := c.ConfirmWithScope(ctx, info)
	return res.Allow, err
}

func (c *tuiConfirm) ConfirmWithScope(ctx context.Context, info agent.ToolCallInfo) (agent.ConfirmResult, error) {
	req := confirmRequest{info: info, resp: make(chan confirmResult)}
	c.program.Send(confirmRequestMsg{req: req})
	select {
	case <-ctx.Done():
		return agent.ConfirmResult{}, ctx.Err()
	case r := <-req.resp:
		return agent.ConfirmResult{Allow: r.allow, Scope: r.scope}, r.err
	}
}

// confirmOption is one selectable answer in the permission prompt.
type confirmOption struct {
	label string
	allow bool
	scope agent.ConfirmScope
}

// confirmPanel renders an interactive permission prompt as a bottom strip.
type confirmPanel struct {
	visible  bool
	selected int // index into options
	options  []confirmOption
	ultra    bool
	info     agent.ToolCallInfo
	resp     chan confirmResult
}

// learnedPatternPreview shows the rule a project/global approval
// would write, making it visible that these options author a permanent rule
// rather than approve a single call. Bash rules are learned verbatim.
func learnedPatternPreview(info agent.ToolCallInfo) string {
	tool := info.ToolCall.Name
	if tool == "bash" {
		return tool + ": " + permissions.LiteralCommandPattern(info.BashCommand())
	}
	path, _ := info.Args["path"].(string)
	if path == "" {
		path, _ = info.ToolCall.Arguments["path"].(string)
	}
	if path != "" {
		return tool + ": " + path
	}
	return tool + ": *"
}

func (p *confirmPanel) show(info agent.ToolCallInfo, resp chan confirmResult) {
	p.visible = true
	p.selected = 0
	p.info = info
	p.resp = resp
	p.ultra = permissions.IsUltraDestructiveCommand(info.BashCommand())
	preview := learnedPatternPreview(info)
	p.options = []confirmOption{
		{label: "Deny", allow: false, scope: agent.ScopeDeny},
		{label: "Once", allow: true, scope: agent.ScopeOnce},
		{label: "Session", allow: true, scope: agent.ScopeSession},
		{label: "Project (" + preview + ")", allow: true, scope: agent.ScopeProject},
	}
	if !p.ultra {
		p.options = append(p.options, confirmOption{label: "Global (" + preview + ")", allow: true, scope: agent.ScopeGlobal})
	}
}

func (p *confirmPanel) hide() {
	p.visible = false
	p.selected = 0
	p.info = agent.ToolCallInfo{}
	p.resp = nil
	p.options = nil
	p.ultra = false
}

func (p *confirmPanel) next() {
	if !p.visible || len(p.options) == 0 {
		return
	}
	p.selected = (p.selected + 1) % len(p.options)
}

func (p *confirmPanel) prev() {
	if !p.visible || len(p.options) == 0 {
		return
	}
	p.selected = (p.selected - 1 + len(p.options)) % len(p.options)
}

func (p *confirmPanel) confirm() agent.ConfirmResult {
	if !p.visible || len(p.options) == 0 {
		return agent.ConfirmResult{Allow: false, Scope: agent.ScopeDeny}
	}
	opt := p.options[p.selected]
	return agent.ConfirmResult{Allow: opt.allow, Scope: opt.scope}
}

func (p *confirmPanel) View(width int) string {
	if !p.visible {
		return ""
	}
	if width <= 0 {
		width = 80
	}

	prompt := fmt.Sprintf("Permission request: %s", p.info.ToolCall.Name)
	if args := FormatArgsPlain(p.info.Args); args != "" {
		prompt += " " + args
	}
	if p.ultra {
		prompt += "\nDestructive command (global allow disabled)"
	}

	rendered := make([]string, len(p.options))
	for i, opt := range p.options {
		rendered[i] = optionStyle(p.selected == i).Render(opt.label)
	}
	options := lipgloss.JoinHorizontal(lipgloss.Left, rendered...)

	hint := styleDim().Render("← → select · Enter confirm · Esc cancel")
	line := lipgloss.JoinHorizontal(lipgloss.Left, options, "    ", hint)

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(colorWarn).
		Padding(0, 1).
		Width(width)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left, prompt, line))
}

func optionStyle(selected bool) lipgloss.Style {
	if selected {
		return lipgloss.NewStyle().
			Background(colorWarn).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Bold(true)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
}
