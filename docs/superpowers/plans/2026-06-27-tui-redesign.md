# Lcoder TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Completely rewrite the Lcoder TUI (`pkg/tui`) to reach the design/interaction quality of the reference project Kocoro, while keeping Lcoder's brand identity and all existing features.

**Architecture:** Single top-level `Model` driven by an explicit `state` enum (startup → input ↔ processing, plus session-picker / extensions overlays). `View()` renders a viewport rebuilt from a `[]block` list; the in-flight stream renders as normal glamour markdown in the tail (zero "pop" on commit). Adaptive Nord + frost-cyan theme. Compact inline tool rendering replaces the tool panel.

**Tech Stack:** Go 1.25, bubbletea, bubbles (textarea/viewport), lipgloss, glamour + chroma (markdown/syntax), sahilm/fuzzy (slash menu), mattn/go-runewidth (CJK width). No new dependencies.

**Source of truth:** `docs/superpowers/specs/2026-06-27-tui-redesign-design.md`.

---

## Conventions for every task

- Run tests from the module root: `cd D:/code_practise/project/lab_pj/Lcoder` then `go test ./pkg/tui/...`.
- Build check: `go build ./...`.
- Commit message style follows the repo: `feat(tui): …`, `test(tui): …`, `refactor(tui): …`.
- All new files are in package `tui` under `pkg/tui/`.
- Co-author trailer on every commit:

```
Co-Authored-By: Claude <noreply@anthropic.com>
```

## File-structure map (locks decomposition)

New files: `theme.go`, `width.go`, `markdown.go`, `block.go`, `toolformat.go`, `statusline.go`, `spinner.go`, `logo.go`, `header.go`, `menu.go`, `commands.go`, `paste.go`, `history.go`, `suggestion.go`, `keys.go`, `view.go`, `events.go`. Rewritten: `model.go`, `input.go`, `messages.go`, `app.go`. Restyled/kept: `sessionpicker.go`, `extensionspanel.go`. Removed at the end: `chat.go`, `styles.go`, `statusbar.go`, `status.go`, `renderer.go`, `toolpanel.go`, `slash_commands.go`, `mode_commands.go`.

Because `chat.go`, `styles.go`, and `toolpanel.go` define symbols the new code replaces (`MessageItem`, `ToolCallItem`, `truncate`, `min`, `FormatArgs`, `Theme`, …), the rewrite proceeds **additively** (new files coexist) until Phase 13, which deletes the old files and the symbols they own. To avoid duplicate-symbol compile errors in the meantime, every helper that already exists in an old file (`truncate`, `min`, `FormatArgs`) is **reused, not redefined**, until Phase 13 relocates it. Each task notes this where relevant.

---

## Phase 0: Width helpers (no dependencies)

### Task 0: CJK-aware width helpers

**Files:**
- Create: `pkg/tui/width.go`
- Test: `pkg/tui/width_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestDisplayWidth(t *testing.T) {
	if got := displayWidth("abc"); got != 3 {
		t.Fatalf("ascii width = %d, want 3", got)
	}
	if got := displayWidth("你好"); got != 4 {
		t.Fatalf("cjk width = %d, want 4", got)
	}
}

func TestTruncateCells(t *testing.T) {
	if got := truncateCells("hello", 10, "…"); got != "hello" {
		t.Fatalf("no-trunc = %q, want hello", got)
	}
	got := truncateCells("你好世界", 5, "…")
	if displayWidth(got) > 5 {
		t.Fatalf("truncated width = %d, want <= 5", displayWidth(got))
	}
}

func TestTruncateCellsSafe(t *testing.T) {
	got := truncateCellsSafe("你好abc", 4)
	if displayWidth(got) > 4 {
		t.Fatalf("safe width = %d, want <= 4", displayWidth(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestDisplayWidth -v`
Expected: FAIL — `undefined: displayWidth`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import "github.com/mattn/go-runewidth"

// displayWidth returns the terminal cell width of PLAIN text (no ANSI escapes).
// For already-styled strings use lipgloss.Width, which strips escapes first.
func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

// truncateCells truncates s so its display width is at most maxCells, appending
// tail (whose width is included in the budget) when truncation occurs. A
// double-width rune is never split across the boundary.
func truncateCells(s string, maxCells int, tail string) string {
	if maxCells <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxCells {
		return s
	}
	return runewidth.Truncate(s, maxCells, tail)
}

// truncateCellsSafe truncates pessimistically: every non-ASCII rune is budgeted
// as 2 cells, so the result can never wrap even when runewidth would undercount.
// Reserve for free-form text in the animated live region; ASCII box-drawing must
// not use it.
func truncateCellsSafe(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	used := 0
	for i, r := range s {
		w := 1
		if r >= 0x80 {
			w = 2
		}
		if used+w > maxCells {
			return s[:i]
		}
		used += w
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run 'TestDisplayWidth|TestTruncateCells|TestTruncateCellsSafe' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/width.go pkg/tui/width_test.go
git commit -m "feat(tui): add CJK-aware display-width helpers"
```

---

## Phase 1: Theme (adaptive Nord + frost accent)

### Task 1: Adaptive semantic color palette

**Files:**
- Create: `pkg/tui/theme.go`
- Test: `pkg/tui/theme_test.go`

Note: the existing `styles.go` defines a `Theme` struct and `ThemeByName`. The new palette is **function-based** (`styleDim()`, `styleAccent()`, …) and does not collide with the `Theme` struct, which is removed in Phase 13. Do not redeclare `Theme` here.

**CONVENTION (resolved during execution):** the style helpers are canonical **Style-returning** — `styleDim() lipgloss.Style` etc. — so call sites render with `styleDim().Render(s)` (and may chain `.Width(n).Bold(true).Render(s)`). Some snippets in Phases 10–11 are written in the shorthand `styleDim("text")`; when implementing those, expand them to `styleDim().Render("text")`. Do NOT change the signatures to take a string (it would break the `.Width().Render()` chains in block.go/toolformat.go).

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestAccentResolves(t *testing.T) {
	// styleAccent must produce a non-empty render for non-empty input.
	if out := styleAccent().Render("x"); out == "" {
		t.Fatal("accent render empty")
	}
}

func TestApplyAccentSwapsColor(t *testing.T) {
	orig := colorAccent
	applyAccent(accentPresets[1]) // ocean
	if colorAccent == orig {
		t.Fatal("applyAccent did not change colorAccent")
	}
	applyAccent(accentPresets[0]) // restore frost
}

func TestIsDarkBackgroundStable(t *testing.T) {
	a := isDarkBackground()
	b := isDarkBackground()
	if a != b {
		t.Fatal("isDarkBackground not stable across calls")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestAccentResolves -v`
Expected: FAIL — `undefined: styleAccent`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
)

var (
	darkBgOnce sync.Once
	darkBg     = true
)

// isDarkBackground reports whether the terminal has a dark background, detected
// once via lipgloss and cached. warmBackgroundColor MUST run before bubbletea
// grabs stdin (in Run/RunWithIO before tea.NewProgram), else the OSC 11 reply is
// swallowed and detection silently falls back to dark.
func isDarkBackground() bool {
	darkBgOnce.Do(func() {
		darkBg = lipgloss.HasDarkBackground()
	})
	return darkBg
}

// warmBackgroundColor forces background detection now, while stdin is still free.
func warmBackgroundColor() { _ = isDarkBackground() }

// Semantic palette. Every color is an AdaptiveColor so the TUI stays readable on
// both dark and light terminals. Light = value shown on a light background.
var (
	colorDim        = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	colorSecondary  = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colorFaint      = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}
	colorSuccess    = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	colorError      = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	colorWarn       = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	// colorAccent — Lcoder frost cyan (Nord). Dark #88C0D0, Light #5E81AC.
	colorAccent     = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"}
	colorInfo       = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	colorSelect     = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}
	colorSelectDesc = lipgloss.AdaptiveColor{Light: "242", Dark: "146"}
	// colorUserBar — subtle background tint for the full-width user bar.
	colorUserBar = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
)

// accentPreset is a selectable accent for /color. Only colorAccent is swapped.
type accentPreset struct {
	name        string
	desc        string
	dark, light string
}

var accentPresets = []accentPreset{
	{"frost", "cyan (default)", "#88C0D0", "#5E81AC"},
	{"ocean", "calm blue", "#5CA8FF", "#1060C9"},
	{"aurora", "green", "#A3BE8C", "#1A8A3A"},
	{"sunset", "warm orange", "#FF9C5C", "#C95A10"},
	{"violet", "purple", "#B98CFF", "#6A30C9"},
}

func applyAccent(p accentPreset) {
	colorAccent = lipgloss.AdaptiveColor{Light: p.light, Dark: p.dark}
}

func styleDim() lipgloss.Style       { return lipgloss.NewStyle().Foreground(colorDim) }
func styleSecondary() lipgloss.Style { return lipgloss.NewStyle().Foreground(colorSecondary) }
func styleFaint() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorFaint) }
func styleSuccess() lipgloss.Style   { return lipgloss.NewStyle().Foreground(colorSuccess) }
func styleError() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorError) }
func styleWarn() lipgloss.Style      { return lipgloss.NewStyle().Foreground(colorWarn) }
func styleAccent() lipgloss.Style    { return lipgloss.NewStyle().Foreground(colorAccent) }
func styleInfo() lipgloss.Style      { return lipgloss.NewStyle().Foreground(colorInfo) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run 'TestAccentResolves|TestApplyAccent|TestIsDarkBackground' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/theme.go pkg/tui/theme_test.go
git commit -m "feat(tui): add adaptive frost-cyan semantic theme"
```

---

## Phase 2: Markdown renderer (glamour + cache)

### Task 2: Cached compact glamour renderer

**Files:**
- Create: `pkg/tui/markdown.go`
- Test: `pkg/tui/markdown_test.go`

Note: the old `renderer.go` defines `RenderWithFallback` / `looksLikeMarkdown`. The new entry point is `renderMarkdown(text, width)` plus a `(text,width)` content cache `renderMarkdownCached`. No symbol collision. `renderer.go` is removed in Phase 13.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBasic(t *testing.T) {
	out := renderMarkdown("# Title\n\nsome **bold** text", 80)
	if out == "" {
		t.Fatal("empty render")
	}
	if strings.Contains(out, "# Title") {
		t.Fatal("heading markdown not transformed")
	}
}

func TestRenderMarkdownCacheHit(t *testing.T) {
	a := renderMarkdownCached("hello `code`", 80)
	b := renderMarkdownCached("hello `code`", 80)
	if a != b {
		t.Fatal("cache returned different output for same input")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	if out := renderMarkdown("", 80); out != "" {
		t.Fatalf("empty input render = %q, want empty", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestRenderMarkdown -v`
Expected: FAIL — `undefined: renderMarkdown`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

var blankLineRe = regexp.MustCompile(`(\n[ \t]*(\x1b\[[0-9;]*m)*[ \t]*){3,}`)

// Renderers cached by (width, dark). compactStyle hardcodes dark-only code
// colors, so light terminals get glamour's tuned light style instead.
var (
	rendererCache   = map[string]*glamour.TermRenderer{}
	rendererCacheMu sync.RWMutex
)

// Rendered-content cache keyed by (width,dark,text) so scroll re-renders are cheap.
var (
	mdContentCache   = map[string]string{}
	mdContentCacheMu sync.RWMutex
)

var compactStyle = ansi.StyleConfig{
	Document:   ansi.StyleBlock{Margin: uintPtr(0)},
	BlockQuote: ansi.StyleBlock{Indent: uintPtr(1), IndentToken: stringPtr("│ "), StylePrimitive: ansi.StylePrimitive{Italic: boolPtr(true)}},
	Paragraph:  ansi.StyleBlock{},
	List:       ansi.StyleList{LevelIndent: 2},
	Heading:    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H1:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true), Italic: boolPtr(true), Underline: boolPtr(true)}},
	H2:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H3:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H4:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H5:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H6:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	Strikethrough: ansi.StylePrimitive{CrossedOut: boolPtr(true)},
	Emph:          ansi.StylePrimitive{Italic: boolPtr(true)},
	Strong:        ansi.StylePrimitive{Bold: boolPtr(true)},
	HorizontalRule: ansi.StylePrimitive{Color: stringPtr("240"), Format: "--------"},
	Item:           ansi.StylePrimitive{BlockPrefix: "• "},
	Enumeration:    ansi.StylePrimitive{BlockPrefix: ". "},
	Task:           ansi.StyleTask{Ticked: "[✓] ", Unticked: "[ ] "},
	Link:           ansi.StylePrimitive{Color: stringPtr("30"), Underline: boolPtr(true)},
	LinkText:       ansi.StylePrimitive{Bold: boolPtr(true)},
	Code:           ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: stringPtr("203")}},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: stringPtr("244")}, Margin: uintPtr(0)},
		Chroma: &ansi.Chroma{
			Text:             ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			Error:            ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), BackgroundColor: stringPtr("#F05B5B")},
			Comment:          ansi.StylePrimitive{Color: stringPtr("#676767")},
			CommentPreproc:   ansi.StylePrimitive{Color: stringPtr("#FF875F")},
			Keyword:          ansi.StylePrimitive{Color: stringPtr("#00AAFF")},
			KeywordReserved:  ansi.StylePrimitive{Color: stringPtr("#FF5FD2")},
			KeywordNamespace: ansi.StylePrimitive{Color: stringPtr("#FF5F87")},
			KeywordType:      ansi.StylePrimitive{Color: stringPtr("#6E6ED8")},
			Operator:         ansi.StylePrimitive{Color: stringPtr("#EF8080")},
			Punctuation:      ansi.StylePrimitive{Color: stringPtr("#E8E8A8")},
			Name:             ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			NameBuiltin:      ansi.StylePrimitive{Color: stringPtr("#FF8EC7")},
			NameTag:          ansi.StylePrimitive{Color: stringPtr("#B083EA")},
			NameAttribute:    ansi.StylePrimitive{Color: stringPtr("#7A7AE6")},
			NameClass:        ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), Underline: boolPtr(true), Bold: boolPtr(true)},
			NameDecorator:    ansi.StylePrimitive{Color: stringPtr("#FFFF87")},
			NameFunction:     ansi.StylePrimitive{Color: stringPtr("#00D787")},
			LiteralNumber:    ansi.StylePrimitive{Color: stringPtr("#6EEFC0")},
			LiteralString:    ansi.StylePrimitive{Color: stringPtr("#C69669")},
			GenericDeleted:   ansi.StylePrimitive{Color: stringPtr("#FD5B5B")},
			GenericInserted:  ansi.StylePrimitive{Color: stringPtr("#00D787")},
			GenericStrong:    ansi.StylePrimitive{Bold: boolPtr(true)},
			GenericSubheading: ansi.StylePrimitive{Color: stringPtr("#777777")},
		},
	},
	Table: ansi.StyleTable{},
}

func getRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 120
	}
	dark := isDarkBackground()
	key := fmt.Sprintf("%d:%t", width, dark)

	rendererCacheMu.RLock()
	if r, ok := rendererCache[key]; ok {
		rendererCacheMu.RUnlock()
		return r
	}
	rendererCacheMu.RUnlock()

	rendererCacheMu.Lock()
	defer rendererCacheMu.Unlock()
	if r, ok := rendererCache[key]; ok {
		return r
	}
	r, err := buildRenderer(width, dark)
	if err != nil {
		return nil
	}
	rendererCache[key] = r
	return r
}

func buildRenderer(width int, dark bool) (*glamour.TermRenderer, error) {
	if dark {
		styleJSON, err := json.Marshal(compactStyle)
		if err != nil {
			return nil, err
		}
		return glamour.NewTermRenderer(
			glamour.WithStylesFromJSONBytes(styleJSON),
			glamour.WithWordWrap(width),
		)
	}
	light := styles.LightStyleConfig
	light.Document.Margin = uintPtr(0)
	return glamour.NewTermRenderer(
		glamour.WithStyles(light),
		glamour.WithWordWrap(width),
	)
}

// renderMarkdown renders markdown to ANSI, falling back to plain text on error.
func renderMarkdown(text string, width int) string {
	r := getRenderer(width)
	if r == nil || text == "" {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	out = blankLineRe.ReplaceAllString(out, "\n\n")
	return strings.TrimRight(out, "\n ")
}

// renderMarkdownCached memoizes renderMarkdown by (width,dark,text).
func renderMarkdownCached(text string, width int) string {
	key := fmt.Sprintf("%d:%t:%s", width, isDarkBackground(), text)
	mdContentCacheMu.RLock()
	if out, ok := mdContentCache[key]; ok {
		mdContentCacheMu.RUnlock()
		return out
	}
	mdContentCacheMu.RUnlock()

	out := renderMarkdown(text, width)

	mdContentCacheMu.Lock()
	mdContentCache[key] = out
	mdContentCacheMu.Unlock()
	return out
}

func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }
func boolPtr(b bool) *bool       { return &b }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestRenderMarkdown -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/markdown.go pkg/tui/markdown_test.go
git commit -m "feat(tui): add cached compact glamour markdown renderer"
```

---

## Phase 3: Tool formatting (compact + expanded + summary)

### Task 3: Friendly labels, key-arg extraction, compact/expanded result, summary

**Files:**
- Create: `pkg/tui/toolformat.go`
- Test: `pkg/tui/toolformat_test.go`

Note: `truncate(s, width)` already exists in `chat.go` and is reused here (do NOT redefine it; Phase 13 relocates it into `width.go`). The friendly-label map uses **Lcoder's** tool names — verify against `pkg/tools` during implementation; the set below matches Lcoder's registered tools (`bash`, `file_read`, `file_write`, `file_edit`, `glob`, `grep`, `directory_list`, `http`, `web_search`, `web_fetch`, `use_skill`).

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"
	"time"
)

func TestFriendlyToolLabel(t *testing.T) {
	if got := friendlyToolLabel("bash"); got != "Running a command" {
		t.Fatalf("bash label = %q", got)
	}
	if got := friendlyToolLabel("unknown_tool"); got != "unknown_tool" {
		t.Fatalf("unknown label = %q, want passthrough", got)
	}
}

func TestToolKeyArg(t *testing.T) {
	if got := toolKeyArg("bash", `{"command":"go test ./..."}`); got != "go test ./..." {
		t.Fatalf("bash keyarg = %q", got)
	}
	if got := toolKeyArg("file_read", `{"path":"main.go"}`); got != "main.go" {
		t.Fatalf("file_read keyarg = %q", got)
	}
}

func TestFormatCompactToolResult(t *testing.T) {
	out := formatCompactToolResult("bash", `{"command":"ls"}`, false, "ok", 1200*time.Millisecond)
	if out == "" {
		t.Fatal("empty compact result")
	}
}

func TestFormatToolSummary(t *testing.T) {
	results := []toolResultEntry{{isError: false}, {isError: true}, {isError: false}}
	out := formatToolSummary(results)
	if out == "" {
		t.Fatal("empty summary")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run 'TestFriendlyToolLabel|TestToolKeyArg|TestFormatCompact|TestFormatToolSummary' -v`
Expected: FAIL — `undefined: friendlyToolLabel` / `toolResultEntry`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// toolResultEntry is the minimal record formatToolSummary needs.
type toolResultEntry struct {
	isError bool
}

// toolKeyArg extracts the most meaningful argument from a tool's JSON args.
func toolKeyArg(toolName string, argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return truncate(argsJSON, 40)
	}
	var key string
	switch toolName {
	case "bash":
		key = strVal(m, "command")
	case "file_read", "file_write", "file_edit", "directory_list":
		key = strVal(m, "path")
	case "glob":
		key = strVal(m, "pattern")
	case "grep":
		key = strVal(m, "pattern")
		if path := strVal(m, "path"); path != "" {
			key += ", " + path
		}
	case "http", "web_fetch":
		key = strVal(m, "url")
	case "web_search":
		key = strVal(m, "query")
	case "use_skill":
		key = strVal(m, "skill_name")
	default:
		for _, f := range []string{"query", "path", "url", "command", "name", "pattern"} {
			if v := strVal(m, f); v != "" {
				key = v
				break
			}
		}
	}
	if key == "" {
		return truncate(argsJSON, 40)
	}
	return truncate(key, 50)
}

var toolFriendlyLabels = map[string]string{
	"bash":           "Running a command",
	"file_read":      "Reading a file",
	"file_write":     "Writing a file",
	"file_edit":      "Editing a file",
	"glob":           "Finding files",
	"grep":           "Searching in files",
	"directory_list": "Listing files",
	"http":           "Calling a service",
	"web_search":     "Searching the web",
	"web_fetch":      "Reading a web page",
	"use_skill":      "Using a skill",
}

func friendlyToolLabel(name string) string {
	if label, ok := toolFriendlyLabels[name]; ok {
		return label
	}
	return name
}

func formatToolCallLabel(name, keyArg string) string {
	label := friendlyToolLabel(name)
	switch {
	case label != name && keyArg != "":
		return label + ": " + keyArg
	case label != name:
		return label
	default:
		return fmt.Sprintf("%s(%s)", name, keyArg)
	}
}

func toolResultBrief(content string, elapsed time.Duration) string {
	var parts []string
	if elapsed > 100*time.Millisecond {
		parts = append(parts, fmt.Sprintf("%.1fs", elapsed.Seconds()))
	}
	return strings.Join(parts, "  ")
}

// formatCompactToolResult renders the single-line tool result.
func formatCompactToolResult(toolName, args string, isError bool, content string, elapsed time.Duration) string {
	keyArg := toolKeyArg(toolName, args)
	dimStyle := styleDim()
	icon := styleSuccess().Render("✓")
	brief := toolResultBrief(content, elapsed)
	if isError {
		icon = styleError().Render("✗")
		brief = truncate(content, 60)
	}
	line := fmt.Sprintf("⏵ %s  %s", formatToolCallLabel(toolName, keyArg), icon)
	if brief != "" {
		line += "  " + brief
	}
	return dimStyle.Render(line)
}

const (
	expandedHeadLines = 8
	expandedTailLines = 4
)

// truncateHeadTail keeps the first head and last tail lines, eliding the middle.
func truncateHeadTail(content string, head, tail int) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= head+tail {
		return strings.Join(lines, "\n")
	}
	hidden := len(lines) - head - tail
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("… +%d lines", hidden))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// formatExpandedToolResult renders the Ctrl+O expanded view.
func formatExpandedToolResult(toolName, args string, isError bool, content string, elapsed time.Duration) string {
	compact := formatCompactToolResult(toolName, args, isError, content, elapsed)
	dimStyle := styleDim()
	bodyStyle := dimStyle
	if isError {
		bodyStyle = styleError()
	}
	var sb strings.Builder
	sb.WriteString(compact)
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  Args: " + truncate(args, 200)))
	body := truncateHeadTail(content, expandedHeadLines, expandedTailLines)
	if body != "" {
		label := "  Result:"
		if isError {
			label = "  Error:"
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render(label))
		for _, ln := range strings.Split(body, "\n") {
			sb.WriteString("\n")
			sb.WriteString(bodyStyle.Render("  " + ln))
		}
	}
	return sb.String()
}

// formatToolSummary renders a single collapsed summary line for a turn.
func formatToolSummary(results []toolResultEntry) string {
	total := len(results)
	if total == 0 {
		return ""
	}
	var errCount int
	for _, r := range results {
		if r.isError {
			errCount++
		}
	}
	dimStyle := styleDim()
	okIcon := styleSuccess().Render("✓")
	errIcon := styleError().Render("✗")
	var line string
	if errCount == 0 {
		line = fmt.Sprintf("⏵ %d tools used  %s", total, okIcon)
	} else {
		line = fmt.Sprintf("⏵ %d tools used  %s%d %s%d", total, okIcon, total-errCount, errIcon, errCount)
	}
	return dimStyle.Render(line)
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run 'TestFriendlyToolLabel|TestToolKeyArg|TestFormatCompact|TestFormatToolSummary' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/toolformat.go pkg/tui/toolformat_test.go
git commit -m "feat(tui): add compact inline tool formatting"
```

---

## Phase 4: Conversation block model

### Task 4: Block type + per-role rendering

**Files:**
- Create: `pkg/tui/block.go`
- Test: `pkg/tui/block_test.go`

A `block` is the new unit of conversation history (replacing `MessageItem` for rendering). The viewport content (Phase 10) is `[]block` rebuilt each frame. Tool blocks carry their raw fields so Ctrl+O can re-render expanded.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestRenderUserBlock(t *testing.T) {
	b := block{kind: blockUser, raw: "hello world"}
	out := b.render(80, false)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("user block missing text: %q", out)
	}
}

func TestRenderAssistantBlockMarkdown(t *testing.T) {
	b := block{kind: blockAssistant, raw: "# Hi\n\ntext"}
	out := b.render(80, false)
	if strings.Contains(out, "# Hi") {
		t.Fatal("assistant block did not render markdown")
	}
}

func TestRenderToolBlockCompactVsExpanded(t *testing.T) {
	b := block{kind: blockTool, toolName: "bash", toolArgs: `{"command":"ls"}`, raw: "file1\nfile2"}
	compact := b.render(80, false)
	expanded := b.render(80, true)
	if compact == expanded {
		t.Fatal("expanded should differ from compact")
	}
}

func TestRenderSystemBlock(t *testing.T) {
	b := block{kind: blockSystem, raw: "switched mode"}
	if out := b.render(80, false); !strings.Contains(out, "switched mode") {
		t.Fatalf("system block missing text: %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run 'TestRenderUserBlock|TestRenderAssistant|TestRenderToolBlock|TestRenderSystem' -v`
Expected: FAIL — `undefined: block`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockSystem
)

// block is one rendered unit of conversation history.
type block struct {
	kind blockKind
	id   string // message ID or tool-call ID (for in-place updates)
	raw  string // user text / assistant markdown / tool result content

	// assistant extras
	thinking string
	usage    *blockUsage

	// tool extras
	toolName string
	toolArgs string
	toolErr  bool
	elapsed  time.Duration
}

type blockUsage struct {
	tokens int
	cost   float64
}

// render returns the styled string for this block at the given width. expanded
// only affects tool blocks (Ctrl+O view).
func (b block) render(width int, expanded bool) string {
	switch b.kind {
	case blockUser:
		bar := lipgloss.NewStyle().
			Background(colorUserBar).
			Foreground(colorSecondary).
			Width(width).
			Padding(0, 1)
		return bar.Render("› " + b.raw)
	case blockAssistant:
		var sb strings.Builder
		if b.thinking != "" {
			sb.WriteString(styleDim().Italic(true).Render("🧠 " + truncate(b.thinking, 200)))
			sb.WriteString("\n\n")
		}
		sb.WriteString(renderMarkdownCached(b.raw, width))
		if b.usage != nil {
			sb.WriteString("\n")
			sb.WriteString(styleDim().Render(fmt.Sprintf(" · %d tokens · $%.4f", b.usage.tokens, b.usage.cost)))
		}
		return sb.String()
	case blockTool:
		if expanded {
			return formatExpandedToolResult(b.toolName, b.toolArgs, b.toolErr, b.raw, b.elapsed)
		}
		return formatCompactToolResult(b.toolName, b.toolArgs, b.toolErr, b.raw, b.elapsed)
	default: // blockSystem
		return styleDim().Italic(true).Render(b.raw)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run 'TestRenderUserBlock|TestRenderAssistant|TestRenderToolBlock|TestRenderSystem' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/block.go pkg/tui/block_test.go
git commit -m "feat(tui): add conversation block model with per-role rendering"
```

---

## Phase 5: Status line and spinner

### Task 5a: Full-width status line

**Files:**
- Create: `pkg/tui/statusline.go`
- Test: `pkg/tui/statusline_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStatusLineFillsWidth(t *testing.T) {
	out := statusLine(40, "▌ build · kimi-k2", "? for commands")
	if lipgloss.Width(out) != 40 {
		t.Fatalf("status line width = %d, want 40", lipgloss.Width(out))
	}
}

func TestStatusLineTruncatesOverflow(t *testing.T) {
	left := "▌ " + string(make([]byte, 0)) + "verylongmodename-that-overflows-the-bar"
	out := statusLine(20, left, "right")
	if lipgloss.Width(out) > 20 {
		t.Fatalf("status line width = %d, want <= 20", lipgloss.Width(out))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestStatusLine -v`
Expected: FAIL — `undefined: statusLine`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// statusLine composes a full-width bar: left caption, a dim ─ filler, right
// caption. If left+right overflow width, the left is truncated (cells-safe) and
// at least one filler dash is kept.
func statusLine(width int, left, right string) string {
	if width <= 0 {
		return ""
	}
	dim := styleDim()
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)

	if lw+rw+1 > width {
		// Truncate left to fit, drop right if still too tight.
		budget := width - rw - 1
		if budget < 1 {
			return dim.Render(truncateCellsSafe(stripANSI(left), width))
		}
		left = truncateCellsSafe(stripANSI(left), budget)
		lw = lipgloss.Width(left)
	}
	fill := width - lw - rw
	if fill < 1 {
		fill = 1
	}
	return left + dim.Render(strings.Repeat("─", fill)) + right
}

// stripANSI removes ANSI escapes so width math on plain text is exact.
func stripANSI(s string) string {
	var sb strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestStatusLine -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/statusline.go pkg/tui/statusline_test.go
git commit -m "feat(tui): add full-width status line"
```

### Task 5b: Spinner (braille glyph + rotating phrase)

**Files:**
- Create: `pkg/tui/spinner.go`
- Test: `pkg/tui/spinner_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestSpinnerGlyphCycles(t *testing.T) {
	s := newSpinner()
	g0 := s.glyph()
	s.frame++
	g1 := s.glyph()
	if g0 == g1 {
		t.Fatal("spinner glyph did not advance")
	}
}

func TestSpinnerPhraseStable(t *testing.T) {
	s := newSpinner()
	p := s.phrase()
	if p == "" {
		t.Fatal("empty phrase")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestSpinner -v`
Expected: FAIL — `undefined: newSpinner`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import tea "github.com/charmbracelet/bubbletea"

var spinnerGlyphs = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var spinnerPhrases = []string{
	"Thinking", "Working", "Crunching", "Reasoning", "Cooking", "Pondering",
}

// spinnerTickMsg drives spinner animation while processing.
type spinnerTickMsg struct{}

type spinner struct {
	frame int
}

func newSpinner() spinner { return spinner{} }

func (s spinner) glyph() string {
	return spinnerGlyphs[s.frame%len(spinnerGlyphs)]
}

// phrase rotates every ~50 frames (frames tick ~100ms → ~5s per phrase).
func (s spinner) phrase() string {
	return spinnerPhrases[(s.frame/50)%len(spinnerPhrases)]
}

// view renders the accent-colored glyph plus the dim phrase + "…".
func (s spinner) view() string {
	return styleAccent().Render(s.glyph()) + " " + styleDim().Render(s.phrase()+"…")
}

// tick schedules the next spinner frame. Caller only batches this in
// stateProcessing so it stops when idle.
func spinnerTick() tea.Cmd {
	return tea.Tick(spinnerInterval, func(_ time.Time) tea.Msg { return spinnerTickMsg{} })
}
```

Note: add imports `"time"` and ensure `spinnerInterval` const is declared:

```go
const spinnerInterval = 100 * time.Millisecond
```

Place the `time` import and const at the top of `spinner.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestSpinner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/spinner.go pkg/tui/spinner_test.go
git commit -m "feat(tui): add braille spinner with rotating phrases"
```

---

## Phase 6: Logo and startup header

### Task 6a: Pixel/half-block logo with draw-in frames

**Files:**
- Create: `pkg/tui/logo.go`
- Test: `pkg/tui/logo_test.go`

The logo is a fixed 4-line pixel mark with a cyan diagonal gradient. `logoFrame(n)` reveals columns progressively for the draw-in animation; the final frame is the full mark.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestLogoFrameLineCount(t *testing.T) {
	for n := 0; n <= logoFrames; n++ {
		out := logoFrame(n)
		lines := strings.Split(stripANSI(out), "\n")
		if len(lines) != logoHeight {
			t.Fatalf("frame %d has %d lines, want %d", n, len(lines), logoHeight)
		}
	}
}

func TestLogoFrameColumnsBounded(t *testing.T) {
	full := logoFrame(logoFrames)
	for _, ln := range strings.Split(stripANSI(full), "\n") {
		if displayWidth(ln) > logoWidth {
			t.Fatalf("line width %d exceeds logoWidth %d", displayWidth(ln), logoWidth)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestLogo -v`
Expected: FAIL — `undefined: logoFrame`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// logoArt is the full pixel/half-block Lcoder mark (4 rows). Each row is the
// same rune-length so the draw-in reveal is uniform.
var logoArt = []string{
	"█▀▀▀▀▀▀█",
	"█ ▀▶_   █",
	"█      █",
	"█▄▄▄▄▄▄█",
}

const (
	logoHeight = 4
	logoWidth  = 9 // max rune cells per row (row 2 has a space pad)
	logoFrames = 8 // number of draw-in steps to full reveal
)

// gradientColors approximate a cyan diagonal sweep, brightest top-left.
var gradientColors = []string{"#8FBCBB", "#88C0D0", "#81A1C1", "#5E81AC"}

// logoFrame returns the logo revealed up to step n (0=hidden, logoFrames=full).
// Hidden columns are rendered as spaces so every frame keeps logoHeight lines.
func logoFrame(n int) string {
	if n > logoFrames {
		n = logoFrames
	}
	reveal := n * logoWidth / logoFrames // columns shown this frame
	var rows []string
	for i, row := range logoArt {
		runes := []rune(row)
		shown := make([]rune, len(runes))
		for j, r := range runes {
			if j < reveal {
				shown[j] = r
			} else {
				shown[j] = ' '
			}
		}
		color := gradientColors[i%len(gradientColors)]
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		rows = append(rows, style.Render(string(shown)))
	}
	return strings.Join(rows, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestLogo -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/logo.go pkg/tui/logo_test.go
git commit -m "feat(tui): add pixel logo with draw-in frames"
```

### Task 6b: Startup header box

**Files:**
- Create: `pkg/tui/header.go`
- Test: `pkg/tui/header_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestHeaderContainsMeta(t *testing.T) {
	h := headerInfo{model: "kimi-k2", cwd: "~/lcoder", version: "0.1"}
	out := renderHeader(h, logoFrames, 80)
	if !strings.Contains(stripANSI(out), "kimi-k2") {
		t.Fatal("header missing model")
	}
	if !strings.Contains(stripANSI(out), "Lcoder") {
		t.Fatal("header missing brand name")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestHeader -v`
Expected: FAIL — `undefined: headerInfo`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// headerInfo carries the right-column metadata for the startup header.
type headerInfo struct {
	model   string
	cwd     string
	version string
}

// renderHeader composes the rounded accent box: logo (left, drawn to frame),
// metadata (right). width bounds the box.
func renderHeader(h headerInfo, frame, width int) string {
	logo := logoFrame(frame)

	meta := lipgloss.JoinVertical(lipgloss.Left,
		styleAccent().Bold(true).Render("Lcoder CLI ")+styleDim().Render("v"+h.version),
		styleDim().Render("model ")+h.model,
		styleDim().Render("cwd   ")+h.cwd,
		styleDim().Render("? for commands"),
	)

	body := lipgloss.JoinHorizontal(lipgloss.Top, logo, "  ", meta)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1)
	if width > 4 {
		box = box.MaxWidth(width)
	}
	return box.Render(body)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestHeader -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/header.go pkg/tui/header_test.go
git commit -m "feat(tui): add startup header box"
```

---

## Phase 7: Input, paste folding, history

### Task 7a: Rewrite composer (auto-grow, accent border, prompt)

**Files:**
- Modify (rewrite): `pkg/tui/input.go`
- Test: `pkg/tui/input_test.go`

The existing `InputModel` keeps the field name `textarea` and method `Value()` (used by `model_test.go`). The rewrite adds auto-grow height (1–6 lines), a `›` prompt, and accent/dim border via the new theme. `View()` takes no `Theme` arg now (uses the function palette).

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestInputAutoGrow(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40)
	m.textarea.SetValue("one\ntwo\nthree")
	if h := m.desiredHeight(); h < 3 {
		t.Fatalf("desiredHeight = %d, want >= 3", h)
	}
}

func TestInputHeightCapped(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40)
	m.textarea.SetValue("a\nb\nc\nd\ne\nf\ng\nh\ni")
	if h := m.desiredHeight(); h > 6 {
		t.Fatalf("desiredHeight = %d, want <= 6", h)
	}
}

func TestInputValue(t *testing.T) {
	m := NewInputModel()
	m.textarea.SetValue("hi")
	if m.Value() != "hi" {
		t.Fatalf("Value = %q", m.Value())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestInput -v`
Expected: FAIL — `undefined: (InputModel).SetWidth` / `desiredHeight`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	inputMinHeight = 1
	inputMaxHeight = 6
)

// InputModel wraps bubbles/textarea for the composer.
type InputModel struct {
	textarea  textarea.Model
	focused   bool
	width     int
	processing bool // dim border while the agent runs
}

func NewInputModel() InputModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message…"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(inputMinHeight)
	ta.SetWidth(80)
	ta.Focus()
	// Strip the textarea's own focused styling so our border owns the frame.
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	return InputModel{textarea: ta, focused: true, width: 80}
}

// SetWidth sets the inner textarea width (border adds 2).
func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textarea.SetWidth(width)
}

// desiredHeight returns the auto-grow height clamped to [min,max].
func (m InputModel) desiredHeight() int {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	if lines < inputMinHeight {
		lines = inputMinHeight
	}
	if lines > inputMaxHeight {
		lines = inputMaxHeight
	}
	return lines
}

// SyncHeight applies desiredHeight to the textarea.
func (m *InputModel) SyncHeight() { m.textarea.SetHeight(m.desiredHeight()) }

func (m *InputModel) SetProcessing(p bool) { m.processing = p }
func (m *InputModel) Focus()               { m.textarea.Focus(); m.focused = true }
func (m *InputModel) Blur()                { m.textarea.Blur(); m.focused = false }
func (m *InputModel) Value() string        { return m.textarea.Value() }

func (m *InputModel) Reset() {
	m.textarea.Reset()
	m.textarea.SetHeight(inputMinHeight)
	m.textarea.Focus()
}

func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// View renders the composer with a rounded border (accent when focused, dim
// when processing).
func (m InputModel) View() string {
	border := colorAccent
	if m.processing {
		border = colorDim
	} else if !m.focused {
		border = colorFaint
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Render(m.textarea.View())
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestInput -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/input.go pkg/tui/input_test.go
git commit -m "feat(tui): rewrite composer with auto-grow and accent border"
```

### Task 7b: Large-paste folding

**Files:**
- Create: `pkg/tui/paste.go`
- Test: `pkg/tui/paste_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestPasteStashRoundTrip(t *testing.T) {
	p := newPasteStash()
	big := strings.Repeat("x", 1500)
	placeholder := p.stash(big)
	if !strings.HasPrefix(placeholder, "[Pasted #1") {
		t.Fatalf("placeholder = %q", placeholder)
	}
	expanded := p.expand("before " + placeholder + " after")
	if !strings.Contains(expanded, big) {
		t.Fatal("expand did not restore original text")
	}
}

func TestPasteSmallNotStashed(t *testing.T) {
	p := newPasteStash()
	if p.shouldStash("short") {
		t.Fatal("short text should not stash")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestPaste -v`
Expected: FAIL — `undefined: newPasteStash`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"fmt"
	"strings"
)

// pasteThreshold is the rune count above which a paste is folded to a
// placeholder. Workload: pasting a large file/log into the composer; symptom
// when it binds: the composer balloons and the layout jumps. Override: bump this
// const.
const pasteThreshold = 1000

type pasteStash struct {
	items map[int]string
	next  int
}

func newPasteStash() *pasteStash {
	return &pasteStash{items: map[int]string{}, next: 1}
}

func (p *pasteStash) shouldStash(s string) bool {
	return len([]rune(s)) > pasteThreshold
}

// stash stores s and returns a placeholder token to insert in the composer.
func (p *pasteStash) stash(s string) string {
	id := p.next
	p.next++
	p.items[id] = s
	return fmt.Sprintf("[Pasted #%d (%d chars)]", id, len([]rune(s)))
}

// expand replaces every placeholder token in text with its stashed content.
func (p *pasteStash) expand(text string) string {
	for id, content := range p.items {
		token := fmt.Sprintf("[Pasted #%d (%d chars)]", id, len([]rune(content)))
		text = strings.ReplaceAll(text, token, content)
	}
	return text
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestPaste -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/paste.go pkg/tui/paste_test.go
git commit -m "feat(tui): add large-paste folding"
```

### Task 7c: Input history navigation

**Files:**
- Create: `pkg/tui/history.go`
- Test: `pkg/tui/history_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestHistoryNavigation(t *testing.T) {
	h := newInputHistory()
	h.add("first")
	h.add("second")
	if got := h.prev(); got != "second" {
		t.Fatalf("prev = %q, want second", got)
	}
	if got := h.prev(); got != "first" {
		t.Fatalf("prev = %q, want first", got)
	}
	if got := h.next(); got != "second" {
		t.Fatalf("next = %q, want second", got)
	}
}

func TestHistoryResetOnAdd(t *testing.T) {
	h := newInputHistory()
	h.add("a")
	_ = h.prev()
	h.add("b")
	if got := h.prev(); got != "b" {
		t.Fatalf("prev after add = %q, want b", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestHistory -v`
Expected: FAIL — `undefined: newInputHistory`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

// inputHistory recalls previously submitted prompts via Up/Down.
type inputHistory struct {
	items []string
	pos   int // index into items; len(items) == "current empty line"
}

func newInputHistory() *inputHistory {
	return &inputHistory{pos: 0}
}

func (h *inputHistory) add(s string) {
	if s == "" {
		return
	}
	h.items = append(h.items, s)
	h.pos = len(h.items)
}

// prev moves toward older entries and returns the entry (or "" if none).
func (h *inputHistory) prev() string {
	if len(h.items) == 0 {
		return ""
	}
	if h.pos > 0 {
		h.pos--
	}
	return h.items[h.pos]
}

// next moves toward newer entries; returns "" past the newest.
func (h *inputHistory) next() string {
	if len(h.items) == 0 {
		return ""
	}
	if h.pos < len(h.items)-1 {
		h.pos++
		return h.items[h.pos]
	}
	h.pos = len(h.items)
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestHistory -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/history.go pkg/tui/history_test.go
git commit -m "feat(tui): add input history navigation"
```

---

## Phase 8: Slash-command fuzzy menu

### Task 8: Fuzzy menu over the command registry

**Files:**
- Create: `pkg/tui/menu.go`
- Test: `pkg/tui/menu_test.go`

The menu reads the command registry defined in Phase 9 (`commandRegistry`). To avoid a circular task dependency, Phase 8 defines the registry type and a minimal seed list here; Phase 9 extends the dispatch. The shared types are `commandEntry` and the package-level `commandRegistry`.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestMenuExactPrefixFirst(t *testing.T) {
	matches := menuMatches("he")
	if len(matches) == 0 {
		t.Fatal("no matches for 'he'")
	}
	if matches[0].entry.Name != "help" {
		t.Fatalf("first match = %q, want help", matches[0].entry.Name)
	}
}

func TestMenuFuzzy(t *testing.T) {
	matches := menuMatches("sesn")
	found := false
	for _, m := range matches {
		if m.entry.Name == "sessions" {
			found = true
		}
	}
	if !found {
		t.Fatal("fuzzy did not match 'sessions' for 'sesn'")
	}
}

func TestMenuRenderHighlights(t *testing.T) {
	matches := menuMatches("hel")
	out := renderMenu(matches, 0, 40)
	if !strings.Contains(stripANSI(out), "help") {
		t.Fatal("menu render missing help")
	}
}

func TestMenuEmptyQueryListsAll(t *testing.T) {
	if len(menuMatches("")) != len(commandRegistry) {
		t.Fatal("empty query should list all commands")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run TestMenu -v`
Expected: FAIL — `undefined: menuMatches` / `commandRegistry`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// commandEntry describes one slash command. Dispatch lives in commands.go.
type commandEntry struct {
	Name        string
	Aliases     []string
	Description string
	Category    string
}

// commandRegistry is the single source of truth for slash commands. Phase 9's
// dispatch switches on Name. Keep names lowercase.
var commandRegistry = []commandEntry{
	{Name: "help", Aliases: []string{"?"}, Description: "Show help", Category: "System"},
	{Name: "sessions", Aliases: []string{"resume", "continue"}, Description: "Switch session", Category: "Session"},
	{Name: "fork", Description: "Fork session", Category: "Session"},
	{Name: "new", Aliases: []string{"clear"}, Description: "New session / clear chat", Category: "Session"},
	{Name: "mode", Description: "Switch agent mode", Category: "Agent"},
	{Name: "modes", Description: "List available modes", Category: "Agent"},
	{Name: "skill", Description: "Trigger a skill", Category: "Agent"},
	{Name: "tools", Description: "Toggle expanded tool view", Category: "View"},
	{Name: "extensions", Aliases: []string{"ext"}, Description: "Toggle extensions panel", Category: "View"},
	{Name: "retry", Description: "Retry last turn", Category: "Action"},
	{Name: "status", Description: "View system status", Category: "System"},
	{Name: "quit", Aliases: []string{"q"}, Description: "Quit", Category: "System"},
}

// menuMatch pairs a command with the fuzzy-matched rune positions for highlight.
type menuMatch struct {
	entry          commandEntry
	matchedIndexes []int
}

// menuMatches returns ranked commands for a query (no leading slash). Exact
// prefix matches sort first, then fuzzy matches by score.
func menuMatches(query string) []menuMatch {
	query = strings.TrimPrefix(strings.TrimSpace(query), "/")
	if query == "" {
		out := make([]menuMatch, len(commandRegistry))
		for i, e := range commandRegistry {
			out[i] = menuMatch{entry: e}
		}
		return out
	}

	var prefix, rest []menuMatch
	names := make([]string, len(commandRegistry))
	for i, e := range commandRegistry {
		names[i] = e.Name
	}
	seen := map[string]bool{}

	for _, e := range commandRegistry {
		if strings.HasPrefix(e.Name, query) {
			n := len(query)
			idx := make([]int, n)
			for i := range idx {
				idx[i] = i
			}
			prefix = append(prefix, menuMatch{entry: e, matchedIndexes: idx})
			seen[e.Name] = true
		}
	}

	for _, fm := range fuzzy.Find(query, names) {
		e := commandRegistry[fm.Index]
		if seen[e.Name] {
			continue
		}
		rest = append(rest, menuMatch{entry: e, matchedIndexes: fm.MatchedIndexes})
	}
	return append(prefix, rest...)
}

// renderMenu draws the dropdown with the selected row highlighted and matched
// characters emphasized.
func renderMenu(matches []menuMatch, selected, width int) string {
	if len(matches) == 0 {
		return ""
	}
	var lines []string
	for i, m := range matches {
		name := highlightMatch(m.entry.Name, m.matchedIndexes)
		desc := styleDim().Render("  " + m.entry.Description)
		row := "/" + name + desc
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

// highlightMatch bolds matched rune positions in name.
func highlightMatch(name string, idx []int) string {
	if len(idx) == 0 {
		return name
	}
	set := map[int]bool{}
	for _, i := range idx {
		set[i] = true
	}
	var sb strings.Builder
	for i, r := range name {
		if set[i] {
			sb.WriteString(styleAccent().Bold(true).Render(string(r)))
		} else {
			sb.WriteString(string(r))
		}
	}
	return sb.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run TestMenu -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/menu.go pkg/tui/menu_test.go
git commit -m "feat(tui): add fuzzy slash-command menu"
```

---

## Phase 9: Command helpers (registry lookup + help)

### Task 9: findCommand + formatCommandHelp

**Files:**
- Create: `pkg/tui/commands.go`
- Test: `pkg/tui/commands_test.go`

`parseSlashCommand`, `parseModeCommand`, and the `SlashCommand` type remain in the old `slash_commands.go` / `mode_commands.go` (still compiled, still satisfying `model_test.go`) until Phase 13, which deletes those files and relocates the two parse funcs **into this file**. Phase 9 adds only the new registry-aware helpers, which do not collide. The actual `(*Model)` dispatch method is written in Phase 10 (it mutates model state).

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"
)

func TestFindCommandByAlias(t *testing.T) {
	e, ok := findCommand("?")
	if !ok || e.Name != "help" {
		t.Fatalf("findCommand(?) = %v, %v", e.Name, ok)
	}
	e, ok = findCommand("resume")
	if !ok || e.Name != "sessions" {
		t.Fatalf("findCommand(resume) = %v, %v", e.Name, ok)
	}
}

func TestFindCommandUnknown(t *testing.T) {
	if _, ok := findCommand("nope"); ok {
		t.Fatal("unknown command matched")
	}
}

func TestFormatCommandHelpGrouped(t *testing.T) {
	out := formatCommandHelp()
	if !strings.Contains(out, "System") || !strings.Contains(out, "/help") {
		t.Fatalf("help missing category/command: %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/ -run 'TestFindCommand|TestFormatCommandHelp' -v`
Expected: FAIL — `undefined: findCommand`.

- [ ] **Step 3: Write minimal implementation**

```go
package tui

import (
	"fmt"
	"strings"
)

// matches reports whether e matches name as primary or alias.
func (e commandEntry) matches(name string) bool {
	if e.Name == name {
		return true
	}
	for _, a := range e.Aliases {
		if a == name {
			return true
		}
	}
	return false
}

// findCommand resolves a name (primary or alias) against the registry.
func findCommand(name string) (commandEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, e := range commandRegistry {
		if e.matches(name) {
			return e, true
		}
	}
	return commandEntry{}, false
}

// formatCommandHelp renders the command palette grouped by category.
func formatCommandHelp() string {
	byCategory := map[string][]commandEntry{}
	var categories []string
	for _, c := range commandRegistry {
		if _, ok := byCategory[c.Category]; !ok {
			categories = append(categories, c.Category)
		}
		byCategory[c.Category] = append(byCategory[c.Category], c)
	}
	var lines []string
	for _, cat := range categories {
		lines = append(lines, cat+":")
		for _, c := range byCategory[cat] {
			line := fmt.Sprintf("  /%-12s %s", c.Name, c.Description)
			if len(c.Aliases) > 0 {
				al := make([]string, len(c.Aliases))
				for i, a := range c.Aliases {
					al[i] = "/" + a
				}
				line += fmt.Sprintf(" (%s)", strings.Join(al, ", "))
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui/ -run 'TestFindCommand|TestFormatCommandHelp' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/commands.go pkg/tui/commands_test.go
git commit -m "feat(tui): add command registry lookup and help formatting"
```

---

## Phase 10: Model state machine, events, view, keys

This phase rewrites `model.go` and adds `events.go`, `view.go`, `keys.go`. It is the integration core. Because it replaces the whole model, the package will **not compile** mid-phase; do the sub-tasks in order and only run the full suite at 10e. Between sub-tasks, run `go build ./pkg/tui/ 2>&1 | head` to track which symbols are still missing — expect failures until 10d.

The old `model.go` is replaced wholesale. The old helper funcs it owned that other (not-yet-deleted) files still need — `messageToItem`, `extractUsage`, `toolResultText`, `toolStatus`, `formatThinking`, `mcpServers`, `formatTokenCount`, `appendOrUpdateMessage`, `MessageItem` usage — are handled as follows: `MessageItem`/`ToolCallItem` stay defined in `chat.go` (removed in Phase 13); the new model uses `block`, not `MessageItem`. Keep `toolResultText`, `extractUsage`, `mcpServers`, `formatTokenCount` by **moving them into `events.go`** (Step in 10b). The old `model.go` is deleted in 10a and its still-needed helpers reappear in `events.go`.

### Task 10a: New Model struct, state enum, NewModel, layout

**Files:**
- Replace: `pkg/tui/model.go`

- [ ] **Step 1: Replace `model.go` with the new model skeleton**

```go
package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/skills"
)

// uiState is the explicit state-machine enum.
type uiState int

const (
	stateStartup uiState = iota
	stateInput
	stateProcessing
	stateSessionPicker
	stateExtensions
)

// Model is the single top-level bubbletea model for the Lcoder TUI.
type Model struct {
	width, height int
	cwd           string

	agent   AgentRunner
	session SessionWriter
	store   SessionStore
	bus     *events.Bus

	unsubscribe func()
	eventCh     chan events.Event

	state uiState

	// Conversation history, rebuilt into the viewport each frame.
	blocks   []block
	viewport viewport.Model

	// Streaming state for the in-flight assistant message.
	streaming    bool
	streamLive   string
	streamMsgID  string
	turnTools    []toolResultEntry

	input    InputModel
	spinner  spinner
	paste    *pasteStash
	history  *inputHistory

	// Slash menu (inline dropdown over the composer within stateInput).
	menuVisible  bool
	menuSelected int

	// Overlays (reused from existing files).
	picker   SessionPickerModel
	extPanel ExtensionsPanelModel

	toolsExpanded bool

	header      headerInfo
	headerFrame int

	model       string
	themeStyle  string
	totalCost   float64
	errMsg      string
	ctxUsage    string
	turnStart   int // spinner frame bookkeeping not needed; kept for elapsed if added

	skills      []skills.Skill
	modeManager *agent.ModeManager

	// suggestion (ghost text) state.
	completedTurns int
	suggestion     string
}

// NewModel keeps the exact signature the call sites and tests rely on.
func NewModel(bus *events.Bus, ag AgentRunner, session SessionWriter, store SessionStore, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, loadedSkills ...skills.Skill) *Model {
	// Theme override: honor explicit "light"/"dark", else auto-detect.
	switch themeStyle {
	case "light":
		darkBgOnce.Do(func() { darkBg = false })
	case "dark":
		darkBgOnce.Do(func() { darkBg = true })
	}
	warmBackgroundColor()

	vp := viewport.New(80, 15)
	m := &Model{
		agent:       ag,
		session:     session,
		store:       store,
		cwd:         cwd,
		bus:         bus,
		eventCh:     make(chan events.Event, 64),
		state:       stateStartup,
		viewport:    vp,
		input:       NewInputModel(),
		spinner:     newSpinner(),
		paste:       newPasteStash(),
		history:     newInputHistory(),
		extPanel:    ExtensionsPanelModel{HTTPTools: httpTools, MCPServers: mcpServers(mcpRegistry)},
		model:       model,
		themeStyle:  themeStyle,
		skills:      loadedSkills,
		modeManager: modeManager,
		header:      headerInfo{model: model, cwd: cwd, version: "0.1"},
	}
	m.unsubscribe = bus.Subscribe(m.onEvent)
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEventCmd(m.eventCh),
		headerTick(),
	)
}

// onEvent is the events.Bus callback; forwards to the channel the UI drains.
// VERIFIED: events.Handler is func(ctx context.Context, event events.Event) error
// and Bus.Subscribe returns the unsubscribe func() (pkg/events/bus.go:10,24).
func (m *Model) onEvent(ctx context.Context, ev events.Event) error {
	select {
	case m.eventCh <- ev:
	case <-ctx.Done():
	}
	return nil
}

// Close cleans up the event subscription.
func (m *Model) Close() {
	if m.unsubscribe != nil {
		m.unsubscribe()
	}
}

// appendBlock adds a block and marks the viewport dirty.
func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
	m.rebuildViewport()
}

// addSystem appends a dim system line.
func (m *Model) addSystem(text string) {
	m.appendBlock(block{kind: blockSystem, raw: text})
}

// addUser appends a full-width user bar.
func (m *Model) addUser(text string) {
	m.appendBlock(block{kind: blockUser, raw: text})
}

// updateSizes recomputes layout after a resize.
func (m *Model) updateSizes() {
	m.input.SetWidth(m.width - 2)
	m.input.SyncHeight()
	bottom := m.bottomHeight()
	vh := m.height - bottom
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vh
	m.rebuildViewport()
}
```

Note on `NewModel`: the signature MUST stay byte-identical to the existing one (`pkg/tui/model.go:57`) so `cmd/lcoder/main.go` and `model_test.go` keep compiling — confirmed during self-review:
`NewModel(bus *events.Bus, agent AgentRunner, session SessionWriter, store SessionStore, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, loadedSkills ...skills.Skill) *Model`.

- [ ] **Step 2: Build check (expect failures for not-yet-defined symbols)**

Run: `go build ./pkg/tui/ 2>&1 | head -30`
Expected: errors only for `headerTick`, `mcpServers`, `bottomHeight`, `rebuildViewport`, `waitForEventCmd` (existing). These are added in 10b–10d.

- [ ] **Step 3: Commit (WIP, compiles after 10d)**

Defer the commit until 10d when the package builds. Track progress with the build check above.

### Task 10b: events.go — streaming event handler + relocated helpers

**Files:**
- Create: `pkg/tui/events.go`

This file owns the `events.Event` → `block` translation that replaces the fragile `UpdateLastAssistant` string-matching. The streaming model: on `MessageStartEvent` for an assistant message, push a fresh `blockAssistant` block and remember its id; on each `MessageUpdateEvent`, append the delta to `streamLive` and overwrite that block's `raw`; on `MessageEndEvent`, commit the final content. Tool events push/patch `blockTool` blocks keyed by `ToolCallID`.

- [ ] **Step 1: Write `events.go`**

```go
package tui

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
)

// handleEvent applies one agent event to the model's block history.
func (m *Model) handleEvent(ev events.Event) {
	switch e := ev.(type) {
	case events.AgentStartEvent:
		m.streaming = false
		m.streamLive = ""
		m.streamMsgID = ""
		m.turnTools = m.turnTools[:0]

	case events.MessageStartEvent:
		if e.Message.Role == models.RoleAssistant {
			m.streaming = true
			m.streamLive = ""
			m.streamMsgID = e.Message.ID
			m.appendBlock(block{kind: blockAssistant, id: e.Message.ID, raw: ""})
		}

	case events.MessageUpdateEvent:
		if !m.streaming {
			break
		}
		m.streamLive += e.Delta
		m.patchAssistant(m.streamLive)

	case events.MessageEndEvent:
		if e.Message.Role == models.RoleAssistant {
			final := e.Message.Text()
			if final == "" {
				final = m.streamLive
			}
			m.commitAssistant(e.Message.ID, final, e.Message.Thinking(), usagePtr(e.Message))
			m.streaming = false
			m.streamLive = ""
			m.streamMsgID = ""
		}

	case events.ToolExecutionStartEvent:
		m.appendBlock(block{kind: blockTool, id: e.ToolCallID, toolName: e.ToolName, toolArgs: e.Args})

	case events.ToolExecutionEndEvent:
		m.finishTool(e.ToolCallID, e.ToolName, e.Result, e.IsError)
		m.turnTools = append(m.turnTools, toolResultEntry{
			name:    e.ToolName,
			isError: e.IsError,
			content: toolResultText(e.Result),
		})

	case events.AgentEndEvent:
		m.completedTurns++

	case events.ErrorEvent:
		m.errMsg = e.Message
		m.addSystem(styleError("error: " + e.Message))
	}
}

// patchAssistant overwrites the raw content of the in-flight assistant block.
func (m *Model) patchAssistant(content string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockAssistant && m.blocks[i].id == m.streamMsgID {
			m.blocks[i].raw = content
			m.rebuildViewport()
			return
		}
	}
}

// commitAssistant finalizes the assistant block with content, thinking, and usage.
func (m *Model) commitAssistant(id, content, thinking string, usage *blockUsage) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockAssistant && m.blocks[i].id == id {
			m.blocks[i].raw = content
			m.blocks[i].thinking = thinking
			m.blocks[i].usage = usage
			if usage != nil {
				m.totalCost += usage.cost
			}
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: blockAssistant, id: id, raw: content, thinking: thinking, usage: usage})
	if usage != nil {
		m.totalCost += usage.cost
	}
}

// finishTool patches the tool block identified by id with its result.
func (m *Model) finishTool(id, name string, result models.ToolResult, isError bool) {
	text := toolResultText(result)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].id == id {
			m.blocks[i].raw = text
			m.blocks[i].toolErr = isError
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: blockTool, id: id, toolName: name, raw: text, toolErr: isError})
}

// --- Relocated helpers (VERIFIED against pkg/models/message.go + old model.go) ---
//
// AgentMessage carries Content []ContentPart, NOT a string. Text is extracted
// via (AgentMessage).Text(); thinking via (AgentMessage).Thinking(). Usage is
// stored in Metadata["usage"], not a struct field. These three helpers are
// copied verbatim from the old model.go (lines 594-617) — do not re-derive them.

// extractUsage pulls LLMUsage from the message metadata. (verbatim from old model.go)
func extractUsage(msg models.AgentMessage) (models.LLMUsage, bool) {
	if msg.Metadata == nil {
		return models.LLMUsage{}, false
	}
	v, ok := msg.Metadata["usage"]
	if !ok {
		return models.LLMUsage{}, false
	}
	u, ok := v.(models.LLMUsage)
	return u, ok
}

// usagePtr adapts extractUsage into the *blockUsage the block renderer wants.
func usagePtr(msg models.AgentMessage) *blockUsage {
	u, ok := extractUsage(msg)
	if !ok {
		return nil
	}
	return &blockUsage{
		inputTokens:  u.PromptTokens,
		outputTokens: u.CompletionTokens,
		totalTokens:  u.TotalTokens,
		cost:         u.TotalCost,
	}
}

// toolResultText renders a ToolResult to plain text. (verbatim from old model.go)
func toolResultText(result models.ToolResult) string {
	var out string
	for _, part := range result.Content {
		if text, ok := part.(models.TextContent); ok {
			out += text.Text
		}
	}
	if len(out) > 200 {
		out = out[:197] + "..."
	}
	return out
}

// mcpServers maps an mcp.Registry to display rows for the extensions panel.
func mcpServers(reg *mcp.Registry) []MCPServerItem {
	if reg == nil {
		return nil
	}
	var out []MCPServerItem
	for _, s := range reg.Servers() {
		out = append(out, MCPServerItem{Name: s.Name, Status: s.Status, Tools: s.ToolCount})
	}
	return out
}

// formatTokenCount renders a token count compactly (1234 -> 1.2k).
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
```

**Field/method verification notes (RESOLVED during plan self-review):**
- Messages on every event are `models.AgentMessage` (confirmed `pkg/events/types.go:46-99`). Use `.Text()` / `.Thinking()` / `.ToolCalls()` / `.Role` / `.ID` — `.Content` is `[]ContentPart`, never a string.
- `LLMUsage` fields confirmed `pkg/models/message.go:347-356`: `PromptTokens`/`CompletionTokens`/`TotalTokens`/`TotalCost`.
- `toolResultText`/`extractUsage` copied verbatim from old `model.go:594-617`.
- `mcp.Registry.Servers()` and `MCPServerItem` field names: STILL must be confirmed against the existing `extensionspanel.go` — reuse its shape; the snippet above is the assumed shape only.

- [ ] **Step 2: Build check**

Run: `go build ./pkg/tui/ 2>&1 | head -30`
Expected: errors now only for `rebuildViewport`, `bottomHeight`, `headerTick` (added in 10c/10d).

### Task 10c: view.go — viewport rebuild, bottom region, View

**Files:**
- Create: `pkg/tui/view.go`

The whole conversation is rebuilt from `[]block` into the viewport every frame the history changes — this is the key simplification that removes streaming "pop". The bottom region (composer + optional menu + status line) is laid out separately and stacked under the viewport. The startup state renders the animated logo/header instead of the viewport.

- [ ] **Step 1: Write `view.go`**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// rebuildViewport re-renders all blocks into the viewport and pins to bottom
// while streaming or when the user is already at the bottom.
func (m *Model) rebuildViewport() {
	atBottom := m.viewport.AtBottom()
	var parts []string
	for _, b := range m.blocks {
		rendered := b.render(m.viewport.Width, m.toolsExpanded)
		if rendered != "" {
			parts = append(parts, rendered)
		}
	}
	m.viewport.SetContent(strings.Join(parts, "\n\n"))
	if m.streaming || atBottom {
		m.viewport.GotoBottom()
	}
}

// bottomHeight reports how many terminal rows the bottom region occupies.
func (m *Model) bottomHeight() int {
	h := m.input.desiredHeight() + 2 // border top+bottom
	h += 1                           // status line
	if m.menuVisible {
		h += menuVisibleRows(m.menuSelected)
	}
	if m.suggestion != "" {
		h += 1
	}
	return h
}

// bottomRegion renders the composer, optional slash menu, suggestion, and status.
func (m *Model) bottomRegion() string {
	var sections []string

	if m.menuVisible {
		matches := menuMatches(m.input.Value())
		sections = append(sections, renderMenu(matches, m.menuSelected, m.width))
	}

	sections = append(sections, m.input.View())

	if m.suggestion != "" {
		ghost := styleFaint("  " + m.suggestion)
		sections = append(sections, ghost)
	}

	sections = append(sections, m.statusLineView())

	return strings.Join(sections, "\n")
}

// statusLineView builds the one-line status bar for the current state.
func (m *Model) statusLineView() string {
	var left, right string
	switch m.state {
	case stateProcessing:
		left = m.spinner.view()
	default:
		left = styleDim(m.modeLabel())
	}
	right = m.contextRight()
	return statusLine(m.width, left, right)
}

// modeLabel returns the current agent mode for the status bar.
func (m *Model) modeLabel() string {
	if mode := m.agent.Mode(); mode != "" {
		return mode
	}
	return "ready"
}

// contextRight builds the right-aligned status segment (model + cost).
func (m *Model) contextRight() string {
	seg := m.model
	if m.totalCost > 0 {
		seg += styleFaint(fmtCost(m.totalCost))
	}
	return styleDim(seg)
}

// View implements tea.Model.
func (m Model) View() string {
	switch m.state {
	case stateStartup:
		return m.startupView()
	case stateSessionPicker:
		return m.picker.View()
	case stateExtensions:
		return m.extPanel.View(m.width, m.height)
	}

	top := m.viewport.View()
	bottom := m.bottomRegion()
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

// startupView renders the animated logo + header over an empty body.
func (m Model) startupView() string {
	logo := logoFrame(m.headerFrame)
	hdr := renderHeader(m.header, m.headerFrame, m.width)
	hint := styleDim("  Press any key to begin")
	body := lipgloss.JoinVertical(lipgloss.Center, logo, "", hdr, "", hint)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// fmtCost formats a dollar cost segment (" · $0.0123").
func fmtCost(c float64) string {
	return lipgloss.NewStyle().Render(" · $" + trimCost(c))
}
```

Note: `menuVisibleRows`, `trimCost`, and `fmtCost` are tiny helpers — define `menuVisibleRows(selected int) int` in `menu.go` (Phase 8 amendment: it returns `min(len(visible)+1, maxMenuRows)`; for the plan, hardcode the cap used in `renderMenu`). Define `trimCost(c float64) string` inline (`fmt.Sprintf("%.4f", c)`). If `renderMenu` already computes its own height, replace `menuVisibleRows` with a call that measures `lipgloss.Height(renderMenu(...))` instead — simpler and always correct. Prefer the measure-by-height approach to avoid drift.

- [ ] **Step 2: Replace the height calc with measure-by-render (recommended)**

To avoid `menuVisibleRows`/`desiredHeight` drift, compute `bottomHeight` by actually rendering and measuring:

```go
func (m *Model) bottomHeight() int {
	return lipgloss.Height(m.bottomRegion())
}
```

This requires `bottomRegion()` to be safe to call before `updateSizes` finishes; guard against `m.width == 0` by returning a minimum of 3. Use this form and delete `menuVisibleRows`.

- [ ] **Step 3: Build check**

Run: `go build ./pkg/tui/ 2>&1 | head -30`
Expected: errors now only for `headerTick` and the `Update`/`handleKey` entrypoints (added in 10d).

### Task 10d: keys.go — Update loop, per-state keys, dispatch, headerTick

**Files:**
- Create: `pkg/tui/keys.go`

This is the dispatcher that makes the package compile. `Update` is the single `tea.Model` entrypoint: it drains agent events, ticks the spinner/header, and routes key messages by `state`. Slash-command dispatch, follow-up-while-processing (`Steer`), and esc-to-interrupt (`Abort`) live here.

- [ ] **Step 1: Write `keys.go`**

```go
package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// headerTickMsg drives the startup logo / header animation.
type headerTickMsg struct{}

func headerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return headerTickMsg{} })
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.header.width = msg.Width
		m.updateSizes()
		return m, nil

	case headerTickMsg:
		m.headerFrame++
		if m.state == stateStartup {
			return m, headerTick()
		}
		return m, nil

	case spinnerTickMsg:
		if m.state == stateProcessing {
			m.spinner.advance()
			return m, spinnerTick()
		}
		return m, nil

	case EventMsg:
		m.handleEvent(msg.Event)
		return m, waitForEventCmd(m.eventCh)

	case AgentDoneMsg:
		m.onAgentDone(msg.Err)
		return m, nil

	case SendPromptMsg:
		return m, m.startPrompt(msg.Text)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Forward everything else to the active overlay or the composer.
	return m.forwardMsg(msg, cmds)
}

// handleKey routes a key by the current state.
func (m *Model) handleKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	// Global quit.
	if k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch m.state {
	case stateStartup:
		m.state = stateInput
		m.updateSizes()
		return m, spinnerTick()

	case stateSessionPicker:
		return m.handlePickerKey(k)

	case stateExtensions:
		if k.Type == tea.KeyEsc {
			m.state = stateInput
		}
		return m, nil

	case stateProcessing:
		return m.handleProcessingKey(k)

	default: // stateInput
		return m.handleInputKey(k)
	}
}

// handleInputKey handles keys while composing.
func (m *Model) handleInputKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	// Slash menu navigation takes precedence when open.
	if m.menuVisible {
		switch k.Type {
		case tea.KeyUp:
			if m.menuSelected > 0 {
				m.menuSelected--
			}
			return m, nil
		case tea.KeyDown:
			m.menuSelected++
			return m, nil
		case tea.KeyTab, tea.KeyEnter:
			return m.acceptMenu()
		case tea.KeyEsc:
			m.menuVisible = false
			return m, nil
		}
	}

	switch k.Type {
	case tea.KeyEnter:
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		// Expand any stashed paste placeholders before submit.
		text = m.paste.expand(text)
		m.input.Reset()
		m.menuVisible = false
		m.history.add(text)
		return m, m.submit(text)

	case tea.KeyUp:
		if prev, ok := m.history.prev(); ok {
			m.input.textarea.SetValue(prev)
		}
		return m, nil

	case tea.KeyDown:
		if next, ok := m.history.next(); ok {
			m.input.textarea.SetValue(next)
		}
		return m, nil
	}

	// Default: let the textarea consume the key, then update menu visibility.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.input.SyncHeight()
	m.refreshMenu()
	m.maybeStashPaste()
	return m, cmd
}

// handleProcessingKey handles keys while the agent runs.
func (m *Model) handleProcessingKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.agent.Abort()
		m.addSystem(styleDim("interrupted"))
		return m, nil
	case tea.KeyCtrlO:
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
		return m, nil
	case tea.KeyEnter:
		// Follow-up while processing: steer the running agent.
		text := strings.TrimSpace(m.input.Value())
		if text != "" {
			m.input.Reset()
			m.addUser(text)
			m.agent.Steer(models.UserMessage(text)) // Steer takes models.AgentMessage
		}
		return m, nil
	}
	// Allow composing a follow-up without submitting.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.input.SyncHeight()
	return m, cmd
}

// submit dispatches a user submission: slash command or agent prompt.
func (m *Model) submit(text string) tea.Cmd {
	if strings.HasPrefix(text, "/") {
		return m.dispatchSlash(text)
	}
	return m.startPrompt(text)
}

// startPrompt records the user block and kicks off the agent.
func (m *Model) startPrompt(text string) tea.Cmd {
	m.addUser(text)
	m.state = stateProcessing
	m.input.SetProcessing(true)
	m.errMsg = ""
	return tea.Batch(
		submitPromptCmd(m.agent, m.session, text),
		spinnerTick(),
	)
}

// onAgentDone returns the model to the input state and persists the session.
func (m *Model) onAgentDone(err error) {
	m.state = stateInput
	m.input.SetProcessing(false)
	if err != nil {
		m.addSystem(styleError("error: " + err.Error()))
	}
	if len(m.turnTools) > 0 {
		m.addSystem(formatToolSummary(m.turnTools))
	}
	m.persistSession()
}

// refreshMenu toggles the slash menu based on the current input.
func (m *Model) refreshMenu() {
	val := m.input.Value()
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") && !strings.Contains(val, "\n") {
		m.menuVisible = true
		if m.menuSelected >= len(menuMatches(val)) {
			m.menuSelected = 0
		}
	} else {
		m.menuVisible = false
		m.menuSelected = 0
	}
}

// acceptMenu fills the composer with the selected command.
func (m *Model) acceptMenu() (*Model, tea.Cmd) {
	matches := menuMatches(m.input.Value())
	if m.menuSelected < len(matches) {
		m.input.textarea.SetValue("/" + matches[m.menuSelected].entry.name + " ")
	}
	m.menuVisible = false
	return m, nil
}

// maybeStashPaste folds an oversized paste into a placeholder.
func (m *Model) maybeStashPaste() {
	val := m.input.Value()
	if m.paste.shouldStash(val) {
		placeholder := m.paste.stash(val)
		m.input.textarea.SetValue(placeholder)
	}
}

// forwardMsg routes non-key messages to the active overlay or composer.
func (m *Model) forwardMsg(msg tea.Msg, cmds []tea.Cmd) (*Model, tea.Cmd) {
	switch m.state {
	case stateSessionPicker:
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}
```

**Verification notes:**
- `m.agent.Steer(models.UserMessage(text))` / `m.agent.Abort()`: VERIFIED on `*agent.Agent` (`loop.go:168` `Steer(msg models.AgentMessage)`, `loop.go:182` `Abort()`). They are NOT on the `AgentRunner` interface yet — extend it in `messages.go` to add `Steer(msg models.AgentMessage)` and `Abort()` (the fake in `model_test.go` gains no-op stubs; see 10e Step 1).
- `submitPromptCmd(m.agent, m.session, text)`: VERIFIED signature `submitPromptCmd(agent AgentRunner, sess SessionWriter, text string) tea.Cmd` (messages.go:27). It wraps text into a `models.AgentMessage` and runs `Prompt` internally, emitting events the UI drains.
- `m.picker.Update` / `handlePickerKey`: reuse the existing sessionpicker API (Phase 12 restyles it). For 10d, stub `handlePickerKey` to delegate to `m.picker.Update(k)` and switch back to `stateInput` on its selection/cancel signal.
- `m.input.SetProcessing` / `desiredHeight` / `SyncHeight`: defined in Phase 7's input.go rewrite.

- [ ] **Step 2: Stub `handlePickerKey` + `dispatchSlash` (full impl in 10? / Phase 12)**

Add minimal versions so the package compiles; full behavior comes with Phase 12 (picker) and the dispatch table below.

```go
func (m *Model) handlePickerKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.picker.Hide()
		m.state = stateInput
		return m, nil
	case tea.KeyEnter:
		if sel := m.picker.Selected(); sel != nil {
			m.loadSession(sel)
		}
		m.picker.Hide()
		m.state = stateInput
		return m, nil
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(k)
	return m, cmd
}
```

**Verification note:** the old model (model.go:236-245) routes Enter through `handlePickerSelect()` which also supports a "fork" mode (`m.picker.mode == "fork"`, `ForkCurrent`). For the redesign, the minimal "load" path above is sufficient; if fork support must be preserved, port `handlePickerSelect` verbatim and call it from the Enter case. `Selected()` returns `*session.Session` (confirmed `sessionpicker.go:99`).

- [ ] **Step 3: Write the slash dispatch (`dispatchSlash`) in keys.go**

```go
// dispatchSlash executes a slash command and returns any follow-up cmd.
func (m *Model) dispatchSlash(text string) tea.Cmd {
	name, rest := parseSlashCommand(text) // reused from old slash_commands.go until Phase 13
	cmd, ok := findCommand(name)
	if !ok {
		m.addSystem(styleError("unknown command: /" + name))
		return nil
	}
	switch cmd.name {
	case "help":
		m.addSystem(formatCommandHelp())
	case "clear":
		m.blocks = nil
		m.rebuildViewport()
	case "sessions":
		m.openSessionPicker()
	case "extensions":
		m.state = stateExtensions
	case "mode":
		m.switchMode(strings.TrimSpace(rest))
	case "model":
		m.addSystem(styleDim("model: " + m.model))
	case "retry":
		return m.retryLast()
	case "quit", "exit":
		return tea.Quit
	default:
		// Mode-style commands (e.g. /plan, /auto) handled via parseModeCommand.
		if mode, ok := parseModeCommand(text); ok {
			m.switchMode(mode)
		} else {
			m.addSystem(styleError("unhandled command: /" + cmd.name))
		}
	}
	return nil
}
```

**Verification notes:**
- `parseSlashCommand` / `parseModeCommand`: reused from the existing `slash_commands.go` / `mode_commands.go` (kept until Phase 13). Confirm their exact return signatures and adapt the destructuring.
- `m.switchMode`, `m.retryLast`, `m.loadSession`, `m.openSessionPicker`, `m.persistSession`: these were methods on the old model. Re-add them as small methods (Step 4) — they mutate the new model's fields.

- [ ] **Step 4: Re-add model methods in keys.go (VERIFIED against old model.go + sessionpicker.go)**

These use the REAL session/picker APIs (confirmed during self-review): `m.session` is a `SessionWriter` whose concrete type is `*session.Session` (it has `.Save()` and `.ActiveMessages()`); `m.agent.SetMessages`/`AllMessages` take/return `[]models.AgentMessage` and are already on the `AgentRunner` interface (no type assertion); `NewSessionPicker(store, cwd, mode, sess)` returns a `SessionPickerModel` with `Visible()`/`Hide()`/`Selected() *session.Session`.

```go
import (
	// ...existing...
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// switchMode changes the agent mode if supported.
func (m *Model) switchMode(mode string) {
	if mode == "" {
		m.addSystem(styleDim("current mode: " + m.modeLabel()))
		return
	}
	if sw, ok := m.agent.(ModeSwitcher); ok {
		m.agent = sw.WithMode(mode) // WithMode returns a NEW runner — must assign
		m.addSystem(styleDim("mode → " + mode))
	} else {
		m.addSystem(styleError("agent does not support modes"))
	}
}

// retryLast prunes the final assistant turn and re-runs the last user prompt.
// Mirrors the old model.go retryLast, adapted to return a tea.Cmd.
func (m *Model) retryLast() tea.Cmd {
	msgs := m.agent.AllMessages()
	var lastUser string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUser = msgs[i].Text()
			break
		}
	}
	if lastUser == "" {
		m.addSystem(styleDim("nothing to retry"))
		return nil
	}
	// Drop trailing assistant/tool messages so the agent re-runs cleanly.
	var pruned []models.AgentMessage
	for _, msg := range msgs {
		pruned = append(pruned, msg)
		if msg.Role == models.RoleUser && msg.Text() == lastUser {
			break
		}
	}
	m.agent.SetMessages(pruned)
	return m.startPrompt(lastUser)
}

// loadSession replaces history with a stored session's messages.
func (m *Model) loadSession(sess *session.Session) {
	if sess == nil {
		return
	}
	msgs := sess.ActiveMessages()
	m.blocks = blocksFromMessages(msgs)
	m.agent.SetMessages(msgs)
	m.rebuildViewport()
}

// openSessionPicker switches to the picker overlay in "load" mode.
func (m *Model) openSessionPicker() {
	var cur *session.Session
	if s, ok := m.session.(*session.Session); ok {
		cur = s
	}
	m.picker = NewSessionPicker(m.store, m.cwd, "load", cur)
	m.state = stateSessionPicker
}

// persistSession saves the current session to disk (no-op if not a *session.Session).
func (m *Model) persistSession() {
	if sess, ok := m.session.(*session.Session); ok {
		_ = sess.Save()
	}
}
```

**Verification notes:**
- `blocksFromMessages([]models.AgentMessage) []block`: new helper in `block.go`/`events.go`. Maps each `AgentMessage` by `.Role`: user→`blockUser{raw: msg.Text()}`, assistant→`blockAssistant{raw: msg.Text(), thinking: msg.Thinking(), usage: usagePtr(msg)}`, plus a `blockTool` per `msg.ToolCalls()`/tool-result message. Reuse `messageToItem`'s field logic (old model.go:578) as the reference for what to surface.
- `ModeSwitcher.WithMode` confirmed `loop.go:228`. Confirm whether `WithMode` returns a value (ignore it) or mutates in place.
- `session.Session` import path: confirm it is `github.com/lcoder/lcoder/pkg/session` (the old model.go imports it — copy that import line exactly).

- [ ] **Step 5: Extend `AgentRunner` with `Steer`/`Abort` in messages.go**

```go
// AgentRunner is the subset of the agent the TUI drives.
// (existing methods unchanged — only Steer/Abort are ADDED. VERIFIED real
// signatures: Steer takes models.AgentMessage; Abort takes no args; both exist
// on *agent.Agent at loop.go:168/182.)
type AgentRunner interface {
	Prompt(ctx context.Context, msg models.AgentMessage) error
	Continue(ctx context.Context) error
	AllMessages() []models.AgentMessage
	SetMessages(msgs []models.AgentMessage)
	Stats() map[string]int
	Mode() string
	Steer(msg models.AgentMessage) // follow-up while processing
	Abort()                        // esc-to-interrupt
}
```

NOTE: `ModeSwitcher.WithMode(mode string) AgentRunner` RETURNS a new runner (it does not mutate in place) — see the corrected `switchMode` in Step 4. The `submitPromptCmd`/`Prompt` path already wraps the raw text into a `models.AgentMessage` internally, so callers pass a string.

(Both methods exist on `*agent.Loop` per `loop.go:168/182`. Adapt the fake in `model_test.go` in 10e.)

- [ ] **Step 6: Full build**

Run: `go build ./pkg/tui/ 2>&1 | head -40`
Iterate until the package compiles. Expect to fix field-name mismatches flagged in the verification notes (token fields, ToolResult fields, picker API). Do NOT silence errors by guessing — open the real type and match it.

- [ ] **Step 7: Commit the compiling core**

```bash
git add pkg/tui/model.go pkg/tui/events.go pkg/tui/view.go pkg/tui/keys.go pkg/tui/messages.go
git commit -m "feat(tui): single-model state machine with streaming blocks"
```

### Task 10e: Adapt model_test.go to the block-based model

**Files:**
- Modify: `pkg/tui/model_test.go`

The old tests assert against `m.messages []MessageItem`. The new model uses `m.blocks []block`. Adapt the assertions (the spec explicitly permits this) while keeping the test *intent* identical: enter → user block recorded; MessageEnd event → assistant block committed; View non-empty; helper funcs (`toolResultText`, `FormatArgs`, `parseModeCommand`, `parseSlashCommand`) still tested.

- [ ] **Step 1: Add `Steer`/`Abort` stubs to the fake agent**

In `model_test.go`'s `fakeAgent`:

```go
func (f *fakeAgent) Steer(models.AgentMessage) {}
func (f *fakeAgent) Abort()                     {}
```

- [ ] **Step 2: Rewrite the enter-submits-user-message test**

```go
func TestModel_EnterSubmitsUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 80, 24
	m.state = stateInput
	m.input.textarea.SetValue("hello")

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var userBlocks int
	var last string
	for _, b := range m2.blocks {
		if b.kind == blockUser {
			userBlocks++
			last = b.raw
		}
	}
	if userBlocks != 1 {
		t.Fatalf("want 1 user block, got %d", userBlocks)
	}
	if last != "hello" {
		t.Fatalf("want raw %q, got %q", "hello", last)
	}
	if m2.state != stateProcessing {
		t.Fatalf("want stateProcessing, got %v", m2.state)
	}
}
```

- [ ] **Step 3: Rewrite the message-end-commits-assistant test**

```go
func TestModel_MessageEndCommitsAssistant(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 80, 24
	m.state = stateProcessing

	start := events.MessageStartEvent{Message: models.AgentMessage{ID: "a1", Role: models.RoleAssistant}}
	m.Update(EventMsg{Event: start})
	end := events.MessageEndEvent{Message: models.AgentMessage{
		ID:      "a1",
		Role:    models.RoleAssistant,
		Content: []models.ContentPart{models.TextContent{Text: "Hi"}},
	}}
	m2, _ := m.Update(EventMsg{Event: end})

	var got string
	var n int
	for _, b := range m2.blocks {
		if b.kind == blockAssistant {
			n++
			got = b.raw
		}
	}
	if n != 1 {
		t.Fatalf("want 1 assistant block, got %d", n)
	}
	if got != "Hi" {
		t.Fatalf("want content %q, got %q", "Hi", got)
	}
}
```

- [ ] **Step 4: Keep the View / helper tests, adjusting only symbol names**

- `TestModel_ViewNotEmpty`: set `m.state = stateInput` first (startup view is also non-empty, but assert against the input view to keep intent). Assert `m.View() != ""` and does not equal the old `"Loading..."` sentinel (which no longer exists — drop that sub-assertion).
- `toolResultText`, `FormatArgs`, `parseModeCommand`, `parseSlashCommand` tests: unchanged (those symbols still exist — `FormatArgs` in toolpanel.go, the parse funcs in their old files, `toolResultText` now in events.go).
- Introduce a `newTestModel(t)` helper if not present, calling `NewModel` with the exact 11-arg signature the old tests used (bus, fakeAgent, fakeSession, fakeStore, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil).

- [ ] **Step 5: Run the package tests**

Run: `go test ./pkg/tui/ 2>&1 | tail -30`
All tests pass. Fix real mismatches (do not weaken assertions beyond the documented adaptations).

- [ ] **Step 6: Commit**

```bash
git add pkg/tui/model_test.go
git commit -m "test(tui): adapt model tests to block-based state machine"
```

**Phase 10 exit criteria:** `go build ./pkg/tui/` succeeds; `go test ./pkg/tui/` passes; the TUI starts, animates the logo, accepts input, streams an assistant reply into a committed block with no visual pop, renders compact tool lines, and esc interrupts a running turn.

---

## Phase 11: Ghost-text follow-up suggestion

**Files:**
- Create: `pkg/tui/suggestion.go`
- Create: `pkg/tui/suggestion_test.go`

A subtle, dim, single-line suggestion shown under the composer after a turn completes — a likely follow-up. It is **gated** (only after ≥1 completed turn, only when the composer is empty, only in `stateInput`) and **graceful**: if no cheap source exists, it is a no-op. The suggestion is purely presentational — pressing `Tab` on an empty composer accepts it; any keystroke that is not Tab dismisses it.

This phase is deliberately conservative: it does NOT call the LLM. It derives a suggestion from a small static heuristic over the last assistant block (e.g. if the assistant asked a question, suggest "yes"/"explain"; otherwise suggest a generic continuation). This keeps it free and synchronous. If a cheap completion source is wired later, swap `deriveSuggestion`'s body — the gating and accept/dismiss flow stay.

- [ ] **Step 1: Write the failing test (`suggestion_test.go`)**

```go
package tui

import "testing"

func TestDeriveSuggestion_Gating(t *testing.T) {
	// No completed turns: no suggestion.
	if s := deriveSuggestion(0, nil); s != "" {
		t.Fatalf("want empty before any turn, got %q", s)
	}
}

func TestDeriveSuggestion_QuestionPromptsAffirmative(t *testing.T) {
	last := &block{kind: blockAssistant, raw: "Do you want me to run the tests?"}
	s := deriveSuggestion(1, last)
	if s == "" {
		t.Fatalf("want a suggestion after a question, got empty")
	}
}

func TestSuggestionAccept(t *testing.T) {
	m := newTestModel(t)
	m.state = stateInput
	m.suggestion = "run the tests"
	m.acceptSuggestion()
	if m.input.Value() != "run the tests" {
		t.Fatalf("want composer filled with suggestion, got %q", m.input.Value())
	}
	if m.suggestion != "" {
		t.Fatalf("want suggestion cleared after accept")
	}
}
```

Run: `go test ./pkg/tui/ -run Suggestion 2>&1 | tail` → fails (symbols undefined).

- [ ] **Step 2: Write `suggestion.go`**

```go
package tui

import "strings"

// deriveSuggestion produces a dim follow-up hint, or "" when not applicable.
// It is intentionally cheap and offline: a small heuristic over the last
// assistant message. Swap the body to wire a real completion source later.
func deriveSuggestion(completedTurns int, last *block) string {
	if completedTurns < 1 || last == nil || last.kind != blockAssistant {
		return ""
	}
	text := strings.TrimSpace(last.raw)
	if text == "" {
		return ""
	}
	// If the assistant asked a question, an affirmative is the likely reply.
	if strings.HasSuffix(text, "?") {
		return "yes"
	}
	return ""
}

// updateSuggestion recomputes the ghost text from current model state.
func (m *Model) updateSuggestion() {
	if m.state != stateInput || strings.TrimSpace(m.input.Value()) != "" {
		m.suggestion = ""
		return
	}
	m.suggestion = deriveSuggestion(m.completedTurns, m.lastAssistantBlock())
}

// lastAssistantBlock returns the most recent assistant block, or nil.
func (m *Model) lastAssistantBlock() *block {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockAssistant {
			return &m.blocks[i]
		}
	}
	return nil
}

// acceptSuggestion moves the ghost text into the composer.
func (m *Model) acceptSuggestion() {
	if m.suggestion == "" {
		return
	}
	m.input.textarea.SetValue(m.suggestion)
	m.suggestion = ""
}
```

- [ ] **Step 3: Wire into the model**

- In `onAgentDone` (keys.go), after `persistSession()`, call `m.updateSuggestion()`.
- In `handleInputKey` (keys.go): if `m.suggestion != ""` and the key is `tea.KeyTab` with an empty composer, call `m.acceptSuggestion()` and return. Any other key: set `m.suggestion = ""` before the default textarea path (so typing dismisses it).
- `bottomRegion` (view.go) already renders `m.suggestion` as a faint line — confirmed in 10c.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/tui/ 2>&1 | tail` → all pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/suggestion.go pkg/tui/suggestion_test.go pkg/tui/keys.go
git commit -m "feat(tui): offline ghost-text follow-up suggestion"
```

**Phase 11 note:** Tab now has two meanings in `stateInput` — accept slash-menu selection (when menu open) and accept suggestion (when menu closed + suggestion present). Order the checks: menu first, then suggestion. Document this in the key handler.

---

## Phase 12: Integrate overlays + alt-screen + background warming

**Files:**
- Modify: `pkg/tui/sessionpicker.go` (restyle only)
- Modify: `pkg/tui/extensionspanel.go` (restyle only)
- Modify: `pkg/tui/app.go` (alt-screen + background warm)

The session picker and extensions panel are kept (their list/selection logic is sound). This phase only restyles them to the new theme tokens and wires alt-screen + one-time background detection. No behavior change to their selection logic.

- [ ] **Step 1: Restyle the session picker to new theme tokens**

Open `sessionpicker.go`. Replace any hardcoded `lipgloss.Color(...)` / old `Theme` field references with the new semantic style funcs:
- selected row → `styleSelect`
- unselected row → `styleDim`
- description/meta → `styleFaint`
- title/header → `styleAccent`

Do NOT change the picker's `Update`/`Selected`/`Done` API — `keys.go` (10d) depends on those names. If the existing picker exposes different signals (e.g. a bool field rather than `Done()`), adapt the 10d `handlePickerKey` to match instead of renaming the picker.

- [ ] **Step 2: Restyle the extensions panel**

Open `extensionspanel.go`. Same treatment: route colors through `styleAccent`/`styleDim`/`styleFaint`/`styleSuccess`/`styleError`. Confirm `ExtensionsPanelModel`, `HTTPToolItem`, `MCPServerItem` field names match what `NewModel` (10a) and `mcpServers` (10b) construct — fix one side to match the other (prefer keeping the existing struct, adjusting 10a/10b).

- [ ] **Step 3: Verify the View entrypoints**

`view.go` (10c) calls `m.picker.View()` and `m.extPanel.View(m.width, m.height)`. Confirm these signatures exist; if the existing `View` takes a `Theme` arg, change it to take `(width, height int)` and drop the theme param (theme is now global). Update the call site if you choose a different signature.

- [ ] **Step 4: Enable alt-screen + warm the background in app.go**

In `app.go`'s `Run`:

```go
func Run(/* existing params */) error {
	warmBackgroundColor() // detect terminal bg ONCE before tea grabs stdin

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
```

- Uncomment / add `tea.WithAltScreen()` (was commented out).
- Call `warmBackgroundColor()` before `tea.NewProgram` — `lipgloss.HasDarkBackground()` queries the terminal, which must happen before bubbletea takes over stdin.
- Leave `RunWithIO` (test entrypoint) WITHOUT alt-screen so tests capture output normally.

- [ ] **Step 5: Build + full test**

Run: `go build ./... 2>&1 | head` then `go test ./pkg/tui/ 2>&1 | tail`
Both clean.

- [ ] **Step 6: Manual smoke test**

Run the binary (`go run ./cmd/lcoder` or the existing entrypoint). Verify: alt-screen engages (terminal scrollback preserved on exit), logo animates, `/sessions` opens the picker and selecting loads history, `/extensions` shows the panel, esc returns to input.

- [ ] **Step 7: Commit**

```bash
git add pkg/tui/sessionpicker.go pkg/tui/extensionspanel.go pkg/tui/app.go
git commit -m "feat(tui): restyle overlays, enable alt-screen and bg warming"
```

---

## Phase 13: Cleanup — delete old files, relocate survivors

**Files:**
- Delete: `pkg/tui/chat.go`, `pkg/tui/styles.go`, `pkg/tui/statusbar.go`, `pkg/tui/status.go`, `pkg/tui/renderer.go`, `pkg/tui/toolpanel.go`, `pkg/tui/slash_commands.go`, `pkg/tui/mode_commands.go`
- Modify: `pkg/tui/commands.go` (absorb relocated funcs), `pkg/tui/width.go` or `block.go` (absorb `truncate`/`min`), `pkg/tui/model_test.go` (drop tests for deleted symbols)

This phase removes the old composed-model machinery now that the state machine fully replaces it. Survivors used by the new code must be relocated FIRST (one commit), then the old files deleted (second commit), so the tree never has duplicate symbols and always compiles.

- [ ] **Step 1: Inventory the survivors before deleting**

Run (verify each is still referenced by the new code):
- `FormatArgs` — used by `block.render` (tool args) and the `FormatArgs` test. Currently in `toolpanel.go`.
- `truncate(s string, width int) string` — used widely (block.go, toolformat.go). Currently in `chat.go`.
- `min(a, b int) int` — used widely. Currently in `chat.go`.
- `parseSlashCommand`, `parseModeCommand`, `SlashCommand` type — used by `dispatchSlash` (keys.go) and their tests. Currently in `slash_commands.go` / `mode_commands.go`.

Use Grep to confirm there are no OTHER survivors before deleting (search each old file's exported symbols across `pkg/tui`).

- [ ] **Step 2: Relocate survivors (commit 1, still compiles)**

- Move `FormatArgs` from `toolpanel.go` → `toolformat.go` (it belongs with tool formatting). Keep the signature identical.
- Move `truncate` and `min` from `chat.go` → `width.go` (string/width helpers). NOTE: `width.go` (Phase 0) already defines `truncateCells`/`displayWidth`; keep `truncate`/`min` too (they are byte/rune-based and used by legacy call paths — or refactor call sites to `truncateCells`. Simplest: keep both, they don't collide).
- Move `parseSlashCommand`, `parseModeCommand`, and the `SlashCommand` type from `slash_commands.go` / `mode_commands.go` → `commands.go`. Keep signatures identical so `model_test.go` and `dispatchSlash` are unchanged.

After moving, the source files still contain their OTHER (now-dead) contents — that's fine for this commit. Run `go build ./pkg/tui/` — must still pass (no duplicate symbols, because you MOVED not copied).

```bash
git add -A pkg/tui/
git commit -m "refactor(tui): relocate surviving helpers ahead of cleanup"
```

- [ ] **Step 3: Delete the old files (commit 2)**

Delete the eight files listed above. Each should now contain only dead code (their survivors already relocated). After deletion:

Run: `go build ./pkg/tui/ 2>&1 | head -40`

Fix any remaining references (a deleted symbol still used somewhere = a missed survivor; relocate it, don't resurrect the file). Common stragglers: `Theme` struct and its constructor (if any new file still references `Theme`, replace with the global style funcs); `RenderWithFallback`/`renderChatContent` (should be fully replaced by `renderMarkdown`/`block.render`).

- [ ] **Step 4: Drop tests for deleted symbols**

In `model_test.go` (and any `*_test.go`), remove tests that reference now-deleted symbols (`MessageItem` rendering, `ChatViewport`, `RenderMessage`, `Theme`). Keep the adapted block-based tests from 10e and the helper tests (`FormatArgs`, `parseSlashCommand`, `parseModeCommand`, `toolResultText`) — their symbols survived.

- [ ] **Step 5: Full build + test + vet**

```bash
go build ./... 2>&1 | head
go test ./... 2>&1 | tail -30
go vet ./pkg/tui/ 2>&1 | head
```

All clean. If `go test ./...` surfaces failures outside `pkg/tui` caused by a changed `NewModel`/`Run` signature, fix the call sites in `cmd/lcoder/main.go` to match (the signature was kept stable on purpose, so this should be minimal).

- [ ] **Step 6: Commit**

```bash
git add -A pkg/tui/ cmd/lcoder/
git commit -m "refactor(tui): remove legacy composed-model files"
```

**Phase 13 exit criteria:** `go build ./...` and `go test ./...` pass; `pkg/tui` contains only the new-architecture files (width, theme, markdown, toolformat, block, statusline, spinner, logo, header, input, paste, history, menu, commands, model, events, view, keys, suggestion, sessionpicker, extensionspanel, app, messages) plus their tests; no `Theme` struct, no `ChatViewport`, no `UpdateLastAssistant`.

---

## Final verification checklist

After Phase 13, confirm the spec is fully realized:

- [ ] Brand identity preserved (name "Lcoder" in logo/header).
- [ ] Frost cyan accent, adaptive light/dark (Dark `#88C0D0` / Light `#5E81AC`).
- [ ] Animated pixel/half-block startup logo (option C) with gradient frames.
- [ ] Animated header (model/cwd/version).
- [ ] Ghost-text follow-up suggestion (offline heuristic, gated).
- [ ] Long-paste folding (placeholder + expand-on-submit).
- [ ] Alt-screen mode (scrollback preserved on exit).
- [ ] Compact inline tool rendering with friendly labels + ✓/✗ + Ctrl+O expand.
- [ ] Streaming with zero "pop" on commit (content-rebuild from blocks).
- [ ] Slash menu (fuzzy, exact-prefix-first) over the composer.
- [ ] esc interrupts a running turn (Abort); Enter-while-processing steers (Steer).
- [ ] Input history (up/down on empty-ish composer).
- [ ] Session picker + extensions panel restyled and functional.
- [ ] All prior Lcoder commands work (`parseSlashCommand`/`parseModeCommand` preserved).
- [ ] Per-`(width,dark)` renderer cache + content cache (no per-frame glamour rebuild).
- [ ] `go build ./...` + `go test ./...` green.

---

## Risks & rollback

- **Risk: agent `Steer`/`Abort` semantics differ from assumption.** Mitigation: Phase 10d verification step reads `loop.go:168/182` before wiring; if `Steer` enqueues vs. interrupts differently, adjust the Enter-while-processing UX (it is non-critical — can fall back to "queue after current turn").
- **Risk: background detection blocks or misreads in some terminals.** Mitigation: `warmBackgroundColor` is one-shot with the `darkBgOnce` guard and honors explicit `--theme light|dark`; default to dark on detection failure.
- **Risk: field-name mismatches (usage/tool-result/picker).** Mitigation: every phase that touches foreign types has an explicit "verify against real type" step; the additive-rewrite ordering means the old, correct call sites remain visible for copy-reference until Phase 13.
- **Rollback:** the whole rewrite is on commits after `f6b1dcd`; `git revert` the Phase 0–13 range or reset to `f6b1dcd` restores the old TUI wholesale (no schema/state migration involved — the TUI is presentation-only).
