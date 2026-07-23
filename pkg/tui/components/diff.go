package components

import "strings"

// DiffLine represents a single line in a diff.
type DiffLine struct {
	Kind string // add | remove | header | context
	Text string
}

// ParseDiff parses a unified-diff-like text into diff lines, classifying each
// line by its leading marker ('+' add, '-' remove, '@' header, else context).
func ParseDiff(text string) []DiffLine {
	var lines []DiffLine
	for _, raw := range strings.Split(text, "\n") {
		if raw == "" {
			continue
		}
		switch raw[0] {
		case '+':
			lines = append(lines, DiffLine{Kind: "add", Text: raw})
		case '-':
			lines = append(lines, DiffLine{Kind: "remove", Text: raw})
		case '@':
			lines = append(lines, DiffLine{Kind: "header", Text: raw})
		default:
			lines = append(lines, DiffLine{Kind: "context", Text: raw})
		}
	}
	return lines
}

// RenderDiff renders diff lines with add/remove/context color coding, one line
// per entry, each clipped to width cells. It renders inline (no box) so it sits
// naturally inside the expanded tool view.
func RenderDiff(lines []DiffLine, width int) string {
	if len(lines) == 0 {
		return styleDim().Render("No diff to display.")
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		text := truncate(line.Text, width)
		switch line.Kind {
		case "add":
			out = append(out, styleSuccess().Render(text))
		case "remove":
			out = append(out, styleError().Render(text))
		case "header":
			out = append(out, styleAccent().Render(text))
		default:
			out = append(out, styleSecondary().Render(text))
		}
	}
	return strings.Join(out, "\n")
}
