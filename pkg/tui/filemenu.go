package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// fileMenuMax caps how many file suggestions the @-picker shows at once.
const fileMenuMax = 10

// activeMentionAt returns the partial path and rune span [at, end) of the
// '@' mention the cursor sits in. The token before the cursor is found by
// scanning back to the last delimiter; it is a mention only when it starts
// with '@'. The cursor may sit anywhere inside the token; end marks the
// token's full extent so a completion can replace the whole word, not just
// the part before the cursor.
func activeMentionAt(text string, cursor int) (partial string, at, end int, ok bool) {
	runes := []rune(text)
	cursor = max(0, min(cursor, len(runes)))
	at = cursor
	for at > 0 && !isMentionSpaceRune(runes[at-1]) {
		at--
	}
	if at >= cursor || runes[at] != '@' {
		return "", 0, 0, false
	}
	end = cursor
	for end < len(runes) && !isMentionSpaceRune(runes[end]) {
		end++
	}
	return string(runes[at+1 : cursor]), at, end, true
}

func isMentionSpaceRune(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// fileMatches lists up to fileMenuMax cwd-relative file paths fuzzy-matching
// partial. It skips .git, node_modules, and hidden directories.
func fileMatches(cwd, partial string) []string {
	var files []string
	_ = filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == cwd {
				return nil
			}
			name := d.Name()
			if name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})

	if partial == "" {
		if len(files) > fileMenuMax {
			files = files[:fileMenuMax]
		}
		return files
	}
	var out []string
	for _, m := range fuzzy.Find(partial, files) {
		out = append(out, files[m.Index])
		if len(out) >= fileMenuMax {
			break
		}
	}
	return out
}

// renderFileMenu draws the @-file suggestion dropdown.
func renderFileMenu(matches []string, selected, width int) string {
	if len(matches) == 0 {
		return ""
	}
	var lines []string
	for i, f := range matches {
		row := "@" + f
		if i == selected {
			row = lipgloss.NewStyle().Foreground(colorSelect).Render("› ") + row
		} else {
			row = "  " + row
		}
		lines = append(lines, truncateCells(row, width, "…"))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint)
	return box.Render(strings.Join(lines, "\n"))
}
