// pkg/tui/markdown/renderer.go
package markdown

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

// darkBg records whether the terminal has a dark background. The TUI warms it
// (SetDarkBackground) before bubbletea grabs stdin so the markdown renderer
// picks the right palette; the safe default is dark (matches the historical
// look). Mirrors the OSC 11 detection in pkg/tui.
var (
	darkBgMu sync.RWMutex
	darkBg   = true
)

// SetDarkBackground records the terminal background kind. Call before bubbletea
// owns stdin; the OSC 11 reply is otherwise swallowed and detection silently
// falls back to dark.
func SetDarkBackground(dark bool) {
	darkBgMu.Lock()
	darkBg = dark
	darkBgMu.Unlock()
}

func isDarkBackground() bool {
	darkBgMu.RLock()
	defer darkBgMu.RUnlock()
	return darkBg
}

// RenderMarkdown renders markdown text to an ANSI string using glamour.
// Inline and display math ($...$ / $$...$$) are converted to code blocks/spans
// before rendering so the formulas are preserved and styled in the terminal.
func RenderMarkdown(source string, width int) string {
	if source == "" {
		return ""
	}
	source = preprocessMath(source)
	r := getMarkdownRenderer(width)
	if r == nil {
		return source
	}
	out, err := r.Render(source)
	if err != nil {
		return source
	}
	return strings.TrimRight(out, "\n ")
}

var (
	rendererCache   = map[string]*glamour.TermRenderer{}
	rendererCacheMu sync.RWMutex
)

// getMarkdownRenderer returns a cached glamour renderer keyed by (width, dark).
// A new combination builds once; resize reuses an existing instance for the
// same width+background. Safe to call from multiple goroutines.
func getMarkdownRenderer(width int) *glamour.TermRenderer {
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

// buildRenderer constructs a glamour renderer for the given width/background.
// Dark terminals use compactStyle (Lcoder cyan accent for inline code/links,
// standard multi-color Chroma syntax highlighting); light terminals use
// glamour's tuned light palette with a zero document margin to keep the compact
// look.
func buildRenderer(width int, dark bool) (*glamour.TermRenderer, error) {
	if dark {
		return glamour.NewTermRenderer(
			glamour.WithStyles(compactStyle),
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

// compactStyle is the dark-terminal markdown style: no document margin, compact
// lists, bold headings, bar-indented blockquotes, and Lcoder cyan for inline
// code and links. The Chroma syntax-highlight palette is the standard
// multi-color set (keyword blue, string amber, comment gray, diff green/red).
var compactStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		// No Color - use the terminal's default foreground. Setting an explicit
		// color dims all text below the terminal default.
		Margin: uintPtr(0),
	},
	BlockQuote: ansi.StyleBlock{
		Indent:      uintPtr(1),
		IndentToken: stringPtr("│ "),
		StylePrimitive: ansi.StylePrimitive{
			Italic: boolPtr(true),
		},
	},
	Paragraph: ansi.StyleBlock{},
	List: ansi.StyleList{
		LevelIndent: 2,
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold:      boolPtr(true),
			Italic:    boolPtr(true),
			Underline: boolPtr(true),
		},
	},
	H2: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H3: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H4: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H5: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H6: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPtr(true),
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPtr(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Color:  stringPtr("240"),
		Format: "--------",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		Ticked:   "[✓] ",
		Unticked: "[ ] ",
	},
	// Link and LinkText use the Lcoder cyan accent (Nord) for brand identity.
	Link: ansi.StylePrimitive{
		Color:     stringPtr("#5E81AC"),
		Underline: boolPtr(true),
	},
	LinkText: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	Image: ansi.StylePrimitive{
		Color:     stringPtr("212"),
		Underline: boolPtr(true),
	},
	// Inline code uses the Lcoder cyan accent (bright Nord cyan) so code spans
	// stand out without a heavy background.
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color: stringPtr("#88C0D0"),
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr("244"),
			},
			Margin: uintPtr(0),
		},
		Chroma: &ansi.Chroma{
			Text:                ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			Error:               ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), BackgroundColor: stringPtr("#F05B5B")},
			Comment:             ansi.StylePrimitive{Color: stringPtr("#676767")},
			CommentPreproc:      ansi.StylePrimitive{Color: stringPtr("#FF875F")},
			Keyword:             ansi.StylePrimitive{Color: stringPtr("#00AAFF")},
			KeywordReserved:     ansi.StylePrimitive{Color: stringPtr("#FF5FD2")},
			KeywordNamespace:    ansi.StylePrimitive{Color: stringPtr("#FF5F87")},
			KeywordType:         ansi.StylePrimitive{Color: stringPtr("#6E6ED8")},
			Operator:            ansi.StylePrimitive{Color: stringPtr("#EF8080")},
			Punctuation:         ansi.StylePrimitive{Color: stringPtr("#E8E8A8")},
			Name:                ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			NameBuiltin:         ansi.StylePrimitive{Color: stringPtr("#FF8EC7")},
			NameTag:             ansi.StylePrimitive{Color: stringPtr("#B083EA")},
			NameAttribute:       ansi.StylePrimitive{Color: stringPtr("#7A7AE6")},
			NameClass:           ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), Underline: boolPtr(true), Bold: boolPtr(true)},
			NameDecorator:       ansi.StylePrimitive{Color: stringPtr("#FFFF87")},
			NameFunction:        ansi.StylePrimitive{Color: stringPtr("#00D787")},
			LiteralNumber:       ansi.StylePrimitive{Color: stringPtr("#6EEFC0")},
			LiteralString:       ansi.StylePrimitive{Color: stringPtr("#C69669")},
			LiteralStringEscape: ansi.StylePrimitive{Color: stringPtr("#AFFFD7")},
			GenericDeleted:      ansi.StylePrimitive{Color: stringPtr("#FD5B5B")},
			GenericEmph:         ansi.StylePrimitive{Italic: boolPtr(true)},
			GenericInserted:     ansi.StylePrimitive{Color: stringPtr("#00D787")},
			GenericStrong:       ansi.StylePrimitive{Bold: boolPtr(true)},
			GenericSubheading:   ansi.StylePrimitive{Color: stringPtr("#777777")},
		},
	},
	Table: ansi.StyleTable{},
}

func uintPtr(i uint) *uint       { return &i }
func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

// preprocessMath converts LaTeX-style math markers into glamour-friendly code
// blocks/spans so the formulas survive markdown rendering. Display math
// ($$...$$) becomes a fenced code block; inline math ($...$) becomes inline
// code. Escaped dollar signs (\$) are left alone.
func preprocessMath(source string) string {
	var sb strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '$' && !isEscaped(source, i) {
			if i+1 < len(source) && source[i+1] == '$' {
				if end := findUnescaped(source, i+2, "$$"); end != -1 {
					sb.WriteString("\n\n```math\n")
					sb.WriteString(source[i+2 : end])
					sb.WriteString("\n```\n\n")
					i = end + 2
					continue
				}
			}
			if end := findUnescaped(source, i+1, "$"); end != -1 {
				sb.WriteString(mathInlineCode(source[i+1 : end]))
				i = end + 1
				continue
			}
		}
		sb.WriteByte(source[i])
		i++
	}
	return sb.String()
}

// isEscaped reports whether the byte at idx is preceded by an odd number of
// backslashes.
func isEscaped(source string, idx int) bool {
	if idx == 0 {
		return false
	}
	backslashes := 0
	for j := idx - 1; j >= 0 && source[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// findUnescaped returns the index of the first occurrence of delim after start
// that is not escaped, or -1 if none is found.
func findUnescaped(source string, start int, delim string) int {
	for i := start; i <= len(source)-len(delim); i++ {
		if source[i:i+len(delim)] == delim && !isEscaped(source, i) {
			return i
		}
	}
	return -1
}

// mathInlineCode wraps math source in backticks, choosing enough backticks to
// avoid clashing with backticks inside the formula.
func mathInlineCode(src string) string {
	max := 0
	cur := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '`' {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur = 0
		}
	}
	ticks := strings.Repeat("`", max+1)
	return ticks + src + ticks
}
