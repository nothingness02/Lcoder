package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// headerInfo carries the right-column metadata for the startup header.
type headerInfo struct {
	model   string
	cwd     string
	version string
}

const headerTotalFrames = 12 // startup swirl reveal + shimmer (~1.4s at 120ms/tick)

// buildTimeLabel returns the running binary's build time (its file mtime) as
// HH:MM. A rebuild does not update an already-running TUI process, so showing
// this makes "am I on the new build?" unambiguous.
func buildTimeLabel() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format("15:04")
}

// renderHeader composes the startup banner: the brand swirl (left, drawn to
// frame) beside model/cwd metadata (right), inside an accent box whose top
// border carries the "Lcoder CLI" title. width bounds the box.
func renderHeader(h headerInfo, frame, width int) string {
	if width < 50 {
		width = 50
	}
	if width > 100 {
		width = 100
	}
	innerWidth := width - 2

	leftLines := renderSwirlGrid(frame)
	// leftWidth is fixed to the widest line of the fully-revealed mark so the box
	// width stays constant while the reveal animation progresses. lipgloss.Width
	// keeps it correct under CJK locales, where half-block runes count as two
	// cells and a naive fixed 16 would misalign the divider.
	leftWidth := 0
	for _, ln := range renderSwirlGrid(headerTotalFrames - 1) {
		if w := lipgloss.Width(ln); w > leftWidth {
			leftWidth = w
		}
	}
	rightWidth := innerWidth - leftWidth - 1 // -1 for the middle divider

	dim := styleDim()
	rightLines := []string{
		" " + styleAccent().Bold(true).Render("Lcoder CLI ") + dim.Render("v"+h.version),
		"",
		" " + dim.Render("model ")+truncate(h.model, rightWidth-9),
		" " + dim.Render("cwd   ")+truncate(h.cwd, rightWidth-9),
		" " + dim.Render("built ")+buildTimeLabel(),
		"",
		" " + dim.Render("? for commands"),
		"",
	}

	// Equalize line counts between columns.
	for len(leftLines) < len(rightLines) {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < len(leftLines) {
		rightLines = append(rightLines, "")
	}

	bdr := lipgloss.NewStyle().Foreground(colorAccent)
	var sb strings.Builder

	// Top border with the embedded title.
	titlePart := "─ Lcoder CLI "
	remaining := innerWidth - lipgloss.Width(titlePart)
	if remaining < 0 {
		remaining = 0
	}
	sb.WriteString(bdr.Render("╭"+titlePart+strings.Repeat("─", remaining)+"╮") + "\n")

	// Content rows: │ left │ right │
	divider := bdr.Render("│")
	for i := range leftLines {
		left := padToWidth(leftLines[i], leftWidth)
		right := padToWidth(rightLines[i], rightWidth)
		sb.WriteString(bdr.Render("│") + left + divider + right + bdr.Render("│") + "\n")
	}

	sb.WriteString(bdr.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))
	return sb.String()
}

// padToWidth pads a (possibly ANSI-styled) string so its visible width reaches
// targetWidth. lipgloss.Width handles ANSI codes and double-width CJK runes.
func padToWidth(styled string, targetWidth int) string {
	visible := lipgloss.Width(styled)
	if visible >= targetWidth {
		return styled
	}
	return styled + strings.Repeat(" ", targetWidth-visible)
}
