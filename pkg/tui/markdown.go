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
	Document:       ansi.StyleBlock{Margin: uintPtr(0)},
	BlockQuote:     ansi.StyleBlock{Indent: uintPtr(1), IndentToken: stringPtr("│ "), StylePrimitive: ansi.StylePrimitive{Italic: boolPtr(true)}},
	Paragraph:      ansi.StyleBlock{},
	List:           ansi.StyleList{LevelIndent: 2},
	Heading:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H1:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true), Italic: boolPtr(true), Underline: boolPtr(true)}},
	H2:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H3:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H4:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H5:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	H6:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
	Strikethrough:  ansi.StylePrimitive{CrossedOut: boolPtr(true)},
	Emph:           ansi.StylePrimitive{Italic: boolPtr(true)},
	Strong:         ansi.StylePrimitive{Bold: boolPtr(true)},
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
			Text:              ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			Error:             ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), BackgroundColor: stringPtr("#F05B5B")},
			Comment:           ansi.StylePrimitive{Color: stringPtr("#676767")},
			CommentPreproc:    ansi.StylePrimitive{Color: stringPtr("#FF875F")},
			Keyword:           ansi.StylePrimitive{Color: stringPtr("#00AAFF")},
			KeywordReserved:   ansi.StylePrimitive{Color: stringPtr("#FF5FD2")},
			KeywordNamespace:  ansi.StylePrimitive{Color: stringPtr("#FF5F87")},
			KeywordType:       ansi.StylePrimitive{Color: stringPtr("#6E6ED8")},
			Operator:          ansi.StylePrimitive{Color: stringPtr("#EF8080")},
			Punctuation:       ansi.StylePrimitive{Color: stringPtr("#E8E8A8")},
			Name:              ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			NameBuiltin:       ansi.StylePrimitive{Color: stringPtr("#FF8EC7")},
			NameTag:           ansi.StylePrimitive{Color: stringPtr("#B083EA")},
			NameAttribute:     ansi.StylePrimitive{Color: stringPtr("#7A7AE6")},
			NameClass:         ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), Underline: boolPtr(true), Bold: boolPtr(true)},
			NameDecorator:     ansi.StylePrimitive{Color: stringPtr("#FFFF87")},
			NameFunction:      ansi.StylePrimitive{Color: stringPtr("#00D787")},
			LiteralNumber:     ansi.StylePrimitive{Color: stringPtr("#6EEFC0")},
			LiteralString:     ansi.StylePrimitive{Color: stringPtr("#C69669")},
			GenericDeleted:    ansi.StylePrimitive{Color: stringPtr("#FD5B5B")},
			GenericInserted:   ansi.StylePrimitive{Color: stringPtr("#00D787")},
			GenericStrong:     ansi.StylePrimitive{Bold: boolPtr(true)},
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
	// Some providers/models return literal "\n" / "\t" sequences in the content
	// string instead of actual control characters. Convert them before markdown
	// rendering so the TUI shows real line breaks and tabs.
	text = strings.ReplaceAll(text, "\\n", "\n")
	text = strings.ReplaceAll(text, "\\t", "\t")

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
