package markdown

import (
	"strings"
	"testing"
)

func TestParseMarkdownNodes(t *testing.T) {
	nodes := Parse("hello\n\n```go\nfmt.Println(1)\n```")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if _, ok := nodes[0].(*TextNode); !ok {
		t.Fatalf("first node should be TextNode, got %T", nodes[0])
	}
	if _, ok := nodes[1].(*CodeBlockNode); !ok {
		t.Fatalf("second node should be CodeBlockNode, got %T", nodes[1])
	}
	code := nodes[1].(*CodeBlockNode)
	if code.Lang != "go" {
		t.Fatalf("lang = %q, want go", code.Lang)
	}
	if !strings.Contains(code.Content, "fmt.Println") {
		t.Fatalf("missing code content")
	}
}
