package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// swirlMask is the Lcoder brand mark — a terminal prompt (▶_) — as a 16×16 ink
// bitmap ('1' = mark), centered in the box. Rendered as 8 terminal lines via
// the half-block technique, filled with the Nord cyan gradient.
var swirlMask = [16]string{
	"0000000000000000",
	"0000000000000000",
	"0001100000000000",
	"0001110000000000",
	"0001111000000000",
	"0001111100000000",
	"0001111110000000",
	"0001111111000000",
	"0001111110000000",
	"0001111100000000",
	"0001111000000000",
	"0001110001111100",
	"0001100001111100",
	"0000000000000000",
	"0000000000000000",
	"0000000000000000",
}

const swirlSize = 16

// Brand gradient endpoints: Nord cyan #5E81AC → #88C0D0 (matches ColorAccent).
var (
	swirlFrom = [3]float64{0x5E, 0x81, 0xAC}
	swirlTo   = [3]float64{0x88, 0xC0, 0xD0}
)

func swirlInk(row, col int) bool { return swirlMask[row][col] == '1' }

// swirlColor returns the brand-gradient color for pixel (col,row). The gradient
// runs diagonally; phase shifts it per startup frame for a subtle shimmer sweep.
func swirlColor(col, row, frame int) lipgloss.Color {
	t := float64(col+row)/float64(2*(swirlSize-1)) + float64(frame)/24.0
	t -= float64(int(t)) // wrap into [0,1)
	r := swirlFrom[0] + (swirlTo[0]-swirlFrom[0])*t
	g := swirlFrom[1] + (swirlTo[1]-swirlFrom[1])*t
	b := swirlFrom[2] + (swirlTo[2]-swirlFrom[2])*t
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", int(r), int(g), int(b)))
}

// renderSwirlGrid renders the 16×16 mark as 8 half-block lines (16 cols each):
// ▀ with fg = upper pixel, bg = lower pixel; ▄ for a lower-only pixel. The mark
// "draws in" top-to-bottom over the first frames, then holds while the gradient
// keeps shimmering — a shape-level animation plus a color sweep on top.
func renderSwirlGrid(frame int) []string {
	revealRows := 6 + frame*2 // frame 0: top 6 rows … frame 5+: all 16
	lines := make([]string, swirlSize/2)
	for i := range lines {
		top, bot := i*2, i*2+1
		var sb strings.Builder
		for col := 0; col < swirlSize; col++ {
			tInk := swirlInk(top, col) && top < revealRows
			bInk := swirlInk(bot, col) && bot < revealRows
			switch {
			case !tInk && !bInk:
				sb.WriteByte(' ')
			case tInk && !bInk:
				sb.WriteString(lipgloss.NewStyle().Foreground(swirlColor(col, top, frame)).Render("▀"))
			case !tInk && bInk:
				sb.WriteString(lipgloss.NewStyle().Foreground(swirlColor(col, bot, frame)).Render("▄"))
			default:
				sb.WriteString(lipgloss.NewStyle().
					Foreground(swirlColor(col, top, frame)).
					Background(swirlColor(col, bot, frame)).Render("▀"))
			}
		}
		lines[i] = sb.String()
	}
	return lines
}
