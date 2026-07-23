package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// shimmer endpoints: resting deep cyan → bright cyan peak — the Lcoder brand
// gradient, so the processing status text glows on-brand instead of a flat dim
// color. Interpolated in RGB so the highlight ramps smoothly; lipgloss
// downsamples to 256/16-color on non-truecolor terminals.
var (
	shimmerBase = [3]int{0x4F, 0x6E, 0x96}
	shimmerPeak = [3]int{0x9B, 0xD8, 0xE8}
)

// renderWaveText renders text with a soft highlight that sweeps across it. Each
// character's brightness follows a gaussian falloff from the moving center, so
// the glow ramps up and down (a raised-cosine "breathing" wave) rather than a
// hard on/off step.
func renderWaveText(text string, tick int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return ""
	}
	// A tail gap (period > n) lets the highlight fully exit before restarting.
	period := n + 6
	center := float64(tick % period)
	const sigma = 2.2 // highlight half-width, in characters

	var sb strings.Builder
	for i, r := range runes {
		d := center - float64(i)
		t := math.Exp(-(d * d) / (2 * sigma * sigma)) // falloff in [0,1]
		cr := shimmerBase[0] + int(float64(shimmerPeak[0]-shimmerBase[0])*t)
		cg := shimmerBase[1] + int(float64(shimmerPeak[1]-shimmerBase[1])*t)
		cb := shimmerBase[2] + int(float64(shimmerPeak[2]-shimmerBase[2])*t)
		hex := fmt.Sprintf("#%02X%02X%02X", cr, cg, cb)
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(string(r)))
	}
	return sb.String()
}
