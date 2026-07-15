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

func TestParseUnorderedList(t *testing.T) {
	nodes := Parse("- one\n- two\n- three")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	list, ok := nodes[0].(*ListNode)
	if !ok {
		t.Fatalf("expected ListNode, got %T", nodes[0])
	}
	if list.Ordered {
		t.Fatalf("expected unordered list")
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list.Items))
	}
	if list.Items[0] != "one" || list.Items[1] != "two" || list.Items[2] != "three" {
		t.Fatalf("unexpected items: %v", list.Items)
	}
}

func TestParseOrderedList(t *testing.T) {
	nodes := Parse("1. first\n2. second\n3. third")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	list, ok := nodes[0].(*ListNode)
	if !ok {
		t.Fatalf("expected ListNode, got %T", nodes[0])
	}
	if !list.Ordered {
		t.Fatalf("expected ordered list")
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list.Items))
	}
	if list.Items[0] != "first" || list.Items[1] != "second" || list.Items[2] != "third" {
		t.Fatalf("unexpected items: %v", list.Items)
	}
}

func TestParseMultipleParagraphs(t *testing.T) {
	nodes := Parse("first paragraph\n\nsecond paragraph\n\nthird paragraph")
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
	for i, node := range nodes {
		text, ok := node.(*TextNode)
		if !ok {
			t.Fatalf("node %d should be TextNode, got %T", i, node)
		}
		want := []string{"first paragraph", "second paragraph", "third paragraph"}[i]
		if text.Text != want {
			t.Fatalf("node %d text = %q, want %q", i, text.Text, want)
		}
	}
}

func TestParseEmptyInput(t *testing.T) {
	nodes := Parse("")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node for empty input, got %d", len(nodes))
	}
	if text, ok := nodes[0].(*TextNode); !ok || text.Text != "" {
		t.Fatalf("expected empty TextNode, got %T %q", nodes[0], text.Text)
	}
}

func TestRenderMarkdownSimple(t *testing.T) {
	out := RenderMarkdown("# Hello\n\nWorld", 80)
	if out == "" {
		t.Fatalf("expected non-empty rendered output")
	}
	if !strings.Contains(out, "Hello") {
		t.Fatalf("expected output to contain Hello, got %q", out)
	}
}

func TestCodeBlockNodeRenderCaches(t *testing.T) {
	node := &CodeBlockNode{Lang: "go", Content: "fmt.Println(1)"}
	out1 := node.Render(80)
	out2 := node.Render(80)
	if out1 != out2 {
		t.Fatalf("cached renders differ:\n%q\n%q", out1, out2)
	}
	key := "80:go:fmt.Println(1)"
	if _, ok := node.cache[key]; !ok {
		t.Fatalf("expected cache entry for key %q", key)
	}
}
