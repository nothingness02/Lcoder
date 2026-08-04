package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

// writeTranscript prints the conversation as plain text after the TUI exits
// the alternate screen, so the session lands in the terminal's native
// scrollback — searchable and draggable with the terminal's own scrollbar.
// Assistant text is the markdown source (no ANSI), system lines are stripped,
// tool blocks collapse to a one-line summary, and the banner is skipped.
func writeTranscript(w io.Writer, blocks []block) {
	var b strings.Builder
	wrote := false
	for _, blk := range blocks {
		switch blk.kind {
		case components.BlockUser:
			fmt.Fprintf(&b, "\n> %s\n", blk.raw)
			wrote = true
		case components.BlockAssistant:
			if strings.TrimSpace(blk.raw) == "" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(blk.raw)
			b.WriteString("\n")
			wrote = true
		case components.BlockTool:
			line := "  • " + blk.toolName
			if blk.toolChip != "" {
				line += " (" + blk.toolChip + ")"
			}
			b.WriteString(line + "\n")
		case components.BlockSystem:
			if s := strings.TrimSpace(stripANSI(blk.raw)); s != "" {
				b.WriteString("  " + s + "\n")
			}
		default: // banner and friends stay in the alt screen
		}
	}
	if !wrote {
		return
	}
	fmt.Fprintf(w, "\n── lcoder transcript %s\n", strings.Repeat("─", 40))
	io.WriteString(w, b.String())
}
