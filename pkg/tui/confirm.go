package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/agent"
)

// confirmResult is returned to the blocked tool call goroutine.
type confirmResult struct {
	allow bool
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
	req := confirmRequest{info: info, resp: make(chan confirmResult)}
	c.program.Send(confirmRequestMsg{req: req})
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case r := <-req.resp:
		return r.allow, r.err
	}
}

// confirmPanel renders an interactive permission prompt as a bottom strip.
type confirmPanel struct {
	visible  bool
	selected int // 0 = Allow, 1 = Deny
	info     agent.ToolCallInfo
	resp     chan confirmResult
}

func (p *confirmPanel) show(info agent.ToolCallInfo, resp chan confirmResult) {
	p.visible = true
	p.selected = 0
	p.info = info
	p.resp = resp
}

func (p *confirmPanel) hide() {
	p.visible = false
	p.selected = 0
	p.info = agent.ToolCallInfo{}
	p.resp = nil
}

func (p *confirmPanel) next() {
	if !p.visible {
		return
	}
	p.selected = (p.selected + 1) % 2
}

func (p *confirmPanel) prev() {
	if !p.visible {
		return
	}
	p.selected = (p.selected - 1 + 2) % 2
}

func (p *confirmPanel) confirm() bool {
	return p.selected == 0
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

	allowStyle := optionStyle(p.selected == 0)
	denyStyle := optionStyle(p.selected == 1)
	options := lipgloss.JoinHorizontal(lipgloss.Left,
		allowStyle.Render("Allow"),
		"  ",
		denyStyle.Render("Deny"),
	)

	hint := styleDim().Render("← → select · Enter confirm · Esc cancel")
	line := lipgloss.JoinHorizontal(lipgloss.Left, options, "    ", hint)

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(colorError).
		Padding(0, 1).
		Width(width)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left, prompt, line))
}

func optionStyle(selected bool) lipgloss.Style {
	if selected {
		return lipgloss.NewStyle().
			Background(colorError).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Bold(true)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
}
