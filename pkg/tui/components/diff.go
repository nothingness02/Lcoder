package components

import (
	"fmt"
	"strings"
)

// diffLineKind classifies a line in a line-level diff.
type diffLineKind int

const (
	diffContext diffLineKind = iota
	diffAdd
	diffDelete
)

// diffLine is one row of a computed diff: delete rows carry the old line
// number, add/context rows the new one (kimi-code's diff-preview shape).
type diffLine struct {
	kind    diffLineKind
	lineNum int
	code    string
}

// maxDiffCells bounds the O(m*n) LCS matrix so a huge overwrite cannot
// allocate an unbounded table; larger inputs fall back to a naive
// all-delete/all-add pairing.
const maxDiffCells = 4_000_000

// computeDiffLines diffs two texts line by line with an LCS dynamic program.
// The backtrack prefers add rows on ties (dp[i][j-1] >= dp[i-1][j]) so delete
// blocks render before the additions that replace them.
func computeDiffLines(oldText, newText string) []diffLine {
	var oldLines, newLines []string
	if oldText != "" {
		oldLines = strings.Split(oldText, "\n")
	}
	if newText != "" {
		newLines = strings.Split(newText, "\n")
	}
	m, n := len(oldLines), len(newLines)

	if m*n > maxDiffCells {
		out := make([]diffLine, 0, m+n)
		for i, ln := range oldLines {
			out = append(out, diffLine{kind: diffDelete, lineNum: i + 1, code: ln})
		}
		for j, ln := range newLines {
			out = append(out, diffLine{kind: diffAdd, lineNum: j + 1, code: ln})
		}
		return out
	}

	stride := n + 1
	dp := make([]int32, (m+1)*stride)
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldLines[i-1] == newLines[j-1] {
				dp[i*stride+j] = dp[(i-1)*stride+j-1] + 1
			} else {
				dp[i*stride+j] = max(dp[(i-1)*stride+j], dp[i*stride+j-1])
			}
		}
	}

	reversed := make([]diffLine, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldLines[i-1] == newLines[j-1]:
			reversed = append(reversed, diffLine{kind: diffContext, lineNum: j, code: newLines[j-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i*stride+j-1] >= dp[(i-1)*stride+j]):
			reversed = append(reversed, diffLine{kind: diffAdd, lineNum: j, code: newLines[j-1]})
			j--
		default:
			reversed = append(reversed, diffLine{kind: diffDelete, lineNum: i, code: oldLines[i-1]})
			i--
		}
	}
	for l, r := 0, len(reversed)-1; l < r; l, r = l+1, r-1 {
		reversed[l], reversed[r] = reversed[r], reversed[l]
	}
	return reversed
}

// diffCluster is an inclusive index range into a diff line slice.
type diffCluster struct{ start, end int }

// buildDiffClusters groups changed lines into clusters: changes at most
// 2*contextLines apart merge, and each cluster extends contextLines of
// context on both sides. Also returns the add/delete counts.
func buildDiffClusters(lines []diffLine, contextLines int) (clusters []diffCluster, added, removed int) {
	var changeIdx []int
	for i, l := range lines {
		switch l.kind {
		case diffAdd:
			added++
			changeIdx = append(changeIdx, i)
		case diffDelete:
			removed++
			changeIdx = append(changeIdx, i)
		}
	}
	if len(changeIdx) == 0 {
		return nil, added, removed
	}
	mergeGap := 2 * contextLines
	groupStart, groupEnd := changeIdx[0], changeIdx[0]
	flush := func() {
		clusters = append(clusters, diffCluster{
			start: max(0, groupStart-contextLines),
			end:   min(len(lines)-1, groupEnd+contextLines),
		})
	}
	for _, idx := range changeIdx[1:] {
		if idx-groupEnd <= mergeGap {
			groupEnd = idx
		} else {
			flush()
			groupStart, groupEnd = idx, idx
		}
	}
	flush()
	return clusters, added, removed
}

// DiffStats counts the added and removed lines between two texts.
func DiffStats(oldText, newText string) (added, removed int) {
	_, added, removed = buildDiffClusters(computeDiffLines(oldText, newText), 3)
	return added, removed
}

// formatDiffRow renders one diff row: dim right-aligned 4-wide line number,
// a colored "+ "/"− " marker for changes, two spaces for context.
func formatDiffRow(line diffLine) string {
	gutter := styleDim().Render(fmt.Sprintf("%4d ", line.lineNum))
	switch line.kind {
	case diffAdd:
		return gutter + styleSuccess().Render("+ "+line.code)
	case diffDelete:
		return gutter + styleError().Render("- "+line.code)
	default:
		return gutter + "  " + line.code
	}
}

// diffExpandHint is the key hint shown when a diff is truncated.
const diffExpandHint = "ctrl+o"

// hiddenChangesFooter renders the truncation notice for hidden change rows.
// An empty expandHint omits the key parenthetical (permission panel, where
// Ctrl+O does not apply).
func hiddenChangesFooter(hidden int, expandHint string) string {
	if hidden <= 0 {
		return ""
	}
	line := fmt.Sprintf("     … %d more change%s hidden", hidden, pluralS(hidden))
	if expandHint != "" {
		line += fmt.Sprintf(" (%s to expand)", expandHint)
	}
	return styleDim().Render(line)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderClusteredRows renders diffLines as cluster rows with `… N unchanged
// lines …` elision between clusters. maxLines<=0 means no cap; the cap counts
// body rows (including elision rows) and may cut mid-cluster so a single huge
// cluster still shows its leading lines. Returns the rows and the number of
// changed lines left hidden by the cap.
func renderClusteredRows(diffLines []diffLine, contextLines, maxLines int) (rows []string, hidden int) {
	clusters, _, _ := buildDiffClusters(diffLines, contextLines)
	changedCount := 0
	for _, l := range diffLines {
		if l.kind != diffContext {
			changedCount++
		}
	}

	cap := maxLines
	if cap <= 0 {
		cap = int(^uint(0) >> 1)
	}
	body := 0
	prevEnd := -1
	truncated := false
	shownChanges := 0

outer:
	for _, cl := range clusters {
		if body >= cap {
			truncated = true
			break
		}
		if prevEnd >= 0 {
			if gap := cl.start - prevEnd - 1; gap > 0 {
				if body+1 > cap {
					truncated = true
					break
				}
				rows = append(rows, styleDim().Render(fmt.Sprintf("     … %d unchanged line%s …", gap, pluralS(gap))))
				body++
			}
		}
		for i := cl.start; i <= cl.end; i++ {
			if body >= cap {
				truncated = true
				break outer
			}
			l := diffLines[i]
			rows = append(rows, formatDiffRow(l))
			body++
			if l.kind != diffContext {
				shownChanges++
			}
			prevEnd = i
		}
	}

	if truncated {
		hidden = changedCount - shownChanges
	}
	return rows, hidden
}

// RenderClusteredDiff renders oldText→newText as clustered diff rows:
// 3 lines of context around each change cluster, `… N unchanged lines …`
// between clusters, and a `… N more changes hidden (<hint> to expand)`
// footer when the maxLines cap (<=0: no cap) truncates the body.
func RenderClusteredDiff(oldText, newText string, maxLines int, expandHint string) []string {
	rows, hidden := renderClusteredRows(computeDiffLines(oldText, newText), 3, maxLines)
	if footer := hiddenChangesFooter(hidden, expandHint); footer != "" {
		rows = append(rows, footer)
	}
	return rows
}
