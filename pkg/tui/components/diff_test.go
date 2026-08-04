package components

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func TestComputeDiffLinesAddDeleteModify(t *testing.T) {
	lines := computeDiffLines("a\nb\nc", "a\nx\nc\nd")
	var kinds []string
	for _, l := range lines {
		kinds = append(kinds, fmt.Sprintf("%d:%s:%d", l.kind, l.code, l.lineNum))
	}
	want := []string{"0:a:1", "2:b:2", "1:x:2", "0:c:3", "1:d:4"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("diff = %v, want %v", kinds, want)
	}
}

func TestComputeDiffLinesPrefersAddOnTie(t *testing.T) {
	// Replacement block: deletes must render before the adds that replace
	// them (backtrack prefers add on ties).
	lines := computeDiffLines("old1\nold2", "new1\nnew2")
	if lines[0].kind != diffDelete || lines[1].kind != diffDelete {
		t.Fatalf("expected deletes first, got %v", lines)
	}
	if lines[2].kind != diffAdd || lines[3].kind != diffAdd {
		t.Fatalf("expected adds after deletes, got %v", lines)
	}
	// Delete rows carry old line numbers, add rows new line numbers.
	if lines[0].lineNum != 1 || lines[1].lineNum != 2 || lines[2].lineNum != 1 || lines[3].lineNum != 2 {
		t.Fatalf("wrong line numbers: %v", lines)
	}
}

func TestComputeDiffLinesEmptySides(t *testing.T) {
	lines := computeDiffLines("", "a\nb")
	if len(lines) != 2 || lines[0].kind != diffAdd || lines[1].kind != diffAdd {
		t.Fatalf("expected all adds, got %v", lines)
	}
	lines = computeDiffLines("a\nb", "")
	if len(lines) != 2 || lines[0].kind != diffDelete || lines[1].kind != diffDelete {
		t.Fatalf("expected all deletes, got %v", lines)
	}
	if lines := computeDiffLines("same", "same"); len(lines) != 1 || lines[0].kind != diffContext {
		t.Fatalf("expected single context line, got %v", lines)
	}
}

func TestBuildDiffClustersMerging(t *testing.T) {
	// Changes with index distance <= 2*contextLines merge; beyond that they
	// split. k intervening context lines mean index distance k+1.
	mk := func(gap int) []diffLine {
		var lines []diffLine
		lines = append(lines, diffLine{kind: diffAdd, lineNum: 1, code: "x"})
		for i := 0; i < gap; i++ {
			lines = append(lines, diffLine{kind: diffContext, lineNum: i + 2, code: "c"})
		}
		lines = append(lines, diffLine{kind: diffAdd, lineNum: gap + 2, code: "y"})
		return lines
	}
	clusters, added, _ := buildDiffClusters(mk(5), 3)
	if len(clusters) != 1 {
		t.Fatalf("index distance 6 should merge into 1 cluster, got %d", len(clusters))
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	clusters, _, _ = buildDiffClusters(mk(6), 3)
	if len(clusters) != 2 {
		t.Fatalf("index distance 7 should split into 2 clusters, got %d", len(clusters))
	}
	// Context padding clamps at the slice bounds.
	if clusters[0].start != 0 {
		t.Fatalf("first cluster start = %d, want 0 (clamped)", clusters[0].start)
	}
}

func TestRenderClusteredDiffElidesUnchanged(t *testing.T) {
	var oldLines, newLines []string
	for i := 1; i <= 30; i++ {
		oldLines = append(oldLines, fmt.Sprintf("line%d", i))
	}
	newLines = append([]string{}, oldLines...)
	newLines[1] = "changed2"
	newLines[20] = "changed21"
	rows := RenderClusteredDiff(strings.Join(oldLines, "\n"), strings.Join(newLines, "\n"), 0, "ctrl+o")
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "+ changed2") || !strings.Contains(joined, "+ changed21") {
		t.Fatalf("expected both changes, got:\n%s", joined)
	}
	if !strings.Contains(joined, "- line2") || !strings.Contains(joined, "- line21") {
		t.Fatalf("expected deletions, got:\n%s", joined)
	}
	if !strings.Contains(joined, "unchanged line") {
		t.Fatalf("expected unchanged-lines elision between clusters, got:\n%s", joined)
	}
	if strings.Contains(joined, "line10") {
		t.Fatalf("unchanged middle should be elided, got:\n%s", joined)
	}
}

func TestRenderClusteredDiffMaxLines(t *testing.T) {
	var oldLines, newLines []string
	for i := 1; i <= 20; i++ {
		oldLines = append(oldLines, fmt.Sprintf("old%d", i))
		newLines = append(newLines, fmt.Sprintf("new%d", i))
	}
	old := strings.Join(oldLines, "\n")
	new := strings.Join(newLines, "\n")

	rows := RenderClusteredDiff(old, new, 10, "ctrl+o")
	// 10 body rows + truncation footer.
	if len(rows) != 11 {
		t.Fatalf("expected 10 rows + footer, got %d:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	last := rows[len(rows)-1]
	if !strings.Contains(last, "more change") || !strings.Contains(last, "ctrl+o to expand") {
		t.Fatalf("expected truncation footer, got %q", last)
	}
	if !strings.Contains(last, "hidden") {
		t.Fatalf("footer should mention hidden changes, got %q", last)
	}

	// No cap: everything renders, no footer.
	full := RenderClusteredDiff(old, new, 0, "ctrl+o")
	if len(full) != 40 {
		t.Fatalf("expected 40 rows without cap, got %d", len(full))
	}
	for _, r := range full {
		if strings.Contains(r, "hidden") {
			t.Fatalf("no footer expected without cap, got %q", r)
		}
	}
}

func TestRenderClusteredDiffLineNumbersAndMarkers(t *testing.T) {
	rows := RenderClusteredDiff("a\nb\nc", "a\nx\nc", 0, "")
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "   1   a") {
		t.Fatalf("context row should have line number and two-space marker, got %q", plain)
	}
	if !strings.Contains(plain, "   2 - b") {
		t.Fatalf("delete row should have old line number, got %q", plain)
	}
	if !strings.Contains(plain, "   2 + x") {
		t.Fatalf("add row should have new line number, got %q", plain)
	}
}

func TestDiffStats(t *testing.T) {
	// LCS keeps "a" and "c": b deleted, x/y/d added.
	added, removed := DiffStats("a\nb\nc", "a\nx\ny\nc\nd")
	if added != 3 || removed != 1 {
		t.Fatalf("DiffStats = +%d -%d, want +3 -1", added, removed)
	}
	if added, removed := DiffStats("same", "same"); added != 0 || removed != 0 {
		t.Fatalf("identical texts should have no changes, got +%d -%d", added, removed)
	}
}
