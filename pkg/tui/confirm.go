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

// confirmPanel renders an interactive permission prompt as a bottom strip.
type confirmPanel struct {
	visible  bool
	selected int // index into options
	options  []string
	ultra    bool
	info     agent.ToolCallInfo
	resp     chan confirmResult
}

func (p *confirmPanel) show(info agent.ToolCallInfo, resp chan confirmResult) {
	p.visible = true
	p.selected = 0
	p.info = info
	p.resp = resp
	p.ultra = permissions.IsUltraDestructiveCommand(info.BashCommand())
	if p.ultra {
		p.options = []string{"Deny", "Once", "Project allow"}
	} else {
		p.options = []string{"Deny", "Once", "Project allow", "Global allow"}
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
	switch p.options[p.selected] {
	case "Once":
		return agent.ConfirmResult{Allow: true, Scope: agent.ScopeOnce}
	case "Project allow":
		return agent.ConfirmResult{Allow: true, Scope: agent.ScopeProject}
	case "Global allow":
		return agent.ConfirmResult{Allow: true, Scope: agent.ScopeGlobal}
	default:
		return agent.ConfirmResult{Allow: false, Scope: agent.ScopeDeny}
	}
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
		rendered[i] = optionStyle(p.selected == i).Render(opt)
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
