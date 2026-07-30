package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestCloseOpenFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no fence", "plain text", "plain text"},
		{"closed pair", "```go\ncode\n```", "```go\ncode\n```"},
		{"open fence gets closed", "```go\ncode", "```go\ncode\n```"},
		{"indented fence counts", "  ```\nx", "  ```\nx\n```"},
		{"two pairs plus open", "```\na\n```\n```\nb", "```\na\n```\n```\nb\n```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := closeOpenFence(c.in); got != c.want {
				t.Errorf("closeOpenFence(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The fence closure must only affect the rendered copy: the block raw stays
// verbatim so commitAssistant sees the true text.
func TestPatchAssistantFenceClosureIsDisplayOnly(t *testing.T) {
	m, _, _ := newTestModel()
	m.streaming = true
	m.streamMsgID = "s1"
	m.appendBlock(block{kind: components.BlockAssistant, id: "s1", raw: ""})

	m.patchAssistant("```go\nfmt.Println()")
	if strings.HasSuffix(m.blocks[0].raw, "```") {
		t.Fatalf("block raw polluted by display closure: %q", m.blocks[0].raw)
	}
	ac := m.components[0].(*components.AssistantComponent)
	out := ac.Render(60, false)
	if !strings.Contains(out, "Println") {
		t.Fatalf("code content missing from render: %q", out)
	}
}
