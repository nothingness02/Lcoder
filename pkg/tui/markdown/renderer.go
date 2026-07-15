package markdown

import (
	"bytes"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Parse converts markdown text into a Node tree.
func Parse(source string) []Node {
	md := goldmark.New()
	reader := text.NewReader([]byte(source))
	root := md.Parser().Parse(reader)

	var nodes []Node
	err := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.FencedCodeBlock:
			lang := string(n.Language(reader.Source()))
			var buf bytes.Buffer
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				buf.Write(seg.Value(reader.Source()))
			}
			nodes = append(nodes, &CodeBlockNode{Lang: lang, Content: buf.String()})
		case *ast.List:
			ordered := n.Marker == '.'
			var items []string
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if li, ok := c.(*ast.ListItem); ok {
					var text bytes.Buffer
					for cc := li.FirstChild(); cc != nil; cc = cc.NextSibling() {
						switch block := cc.(type) {
						case *ast.Paragraph:
							for l := 0; l < block.Lines().Len(); l++ {
								seg := block.Lines().At(l)
								text.Write(seg.Value(reader.Source()))
							}
						case *ast.TextBlock:
							for l := 0; l < block.Lines().Len(); l++ {
								seg := block.Lines().At(l)
								text.Write(seg.Value(reader.Source()))
							}
						}
					}
					items = append(items, strings.TrimSpace(text.String()))
				}
			}
			nodes = append(nodes, &ListNode{Ordered: ordered, Items: items})
		case *ast.Heading:
			var buf bytes.Buffer
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				buf.Write(seg.Value(reader.Source()))
			}
			nodes = append(nodes, &HeadingNode{Level: n.Level, Text: strings.TrimSpace(buf.String())})
		case *ast.Paragraph:
			if isInsideList(n) {
				return ast.WalkSkipChildren, nil
			}
			var buf bytes.Buffer
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				buf.Write(seg.Value(reader.Source()))
			}
			nodes = append(nodes, &TextNode{Text: strings.TrimSpace(buf.String())})
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return []Node{&TextNode{Text: source}}
	}
	if len(nodes) == 0 {
		return []Node{&TextNode{Text: source}}
	}
	return nodes
}

func isInsideList(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*ast.List); ok {
			return true
		}
	}
	return false
}

// RenderMarkdown renders markdown text to an ANSI string using glamour.
func RenderMarkdown(source string, width int) string {
	if source == "" {
		return ""
	}
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
	mdRendererCache   = map[int]*glamour.TermRenderer{}
	mdRendererCacheMu sync.RWMutex

	widthStyleCache   = map[int]lipgloss.Style{}
	widthStyleCacheMu sync.RWMutex
)

func getMarkdownRenderer(width int) *glamour.TermRenderer {
	mdRendererCacheMu.RLock()
	if r, ok := mdRendererCache[width]; ok {
		mdRendererCacheMu.RUnlock()
		return r
	}
	mdRendererCacheMu.RUnlock()

	mdRendererCacheMu.Lock()
	defer mdRendererCacheMu.Unlock()
	if r, ok := mdRendererCache[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRendererCache[width] = r
	return r
}

// widthStyle returns a cached lipgloss style constrained to the given width.
func widthStyle(width int) lipgloss.Style {
	widthStyleCacheMu.RLock()
	if s, ok := widthStyleCache[width]; ok {
		widthStyleCacheMu.RUnlock()
		return s
	}
	widthStyleCacheMu.RUnlock()

	widthStyleCacheMu.Lock()
	defer widthStyleCacheMu.Unlock()
	if s, ok := widthStyleCache[width]; ok {
		return s
	}
	s := lipgloss.NewStyle().Width(width)
	widthStyleCache[width] = s
	return s
}

func renderCodeBlock(lang, content string, width int) string {
	md := "```" + lang + "\n" + content + "\n```"
	return RenderMarkdown(md, width)
}
