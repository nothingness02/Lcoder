package markdown

import (
	"bytes"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Parse converts markdown text into a Node tree.
func Parse(source string) []Node {
	source = strings.ReplaceAll(source, "\\n", "\n")
	source = strings.ReplaceAll(source, "\\t", "\t")
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
						if para, ok := cc.(*ast.Paragraph); ok {
							for l := 0; l < para.Lines().Len(); l++ {
								seg := para.Lines().At(l)
								text.Write(seg.Value(reader.Source()))
							}
						}
					}
					items = append(items, strings.TrimSpace(text.String()))
				}
			}
			nodes = append(nodes, &ListNode{Ordered: ordered, Items: items})
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
func RenderMarkdown(text string, width int) string {
	return renderMarkdown(text, width)
}

func renderMarkdown(text string, width int) string {
	if text == "" {
		return ""
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n ")
}

func renderCodeBlock(lang, content string, width int) string {
	md := "```" + lang + "\n" + content + "\n```"
	return renderMarkdown(md, width)
}
