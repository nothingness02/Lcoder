package markdown

// Node is a renderable unit inside a parsed markdown tree.
type Node interface {
	Height(width int) int
	Render(width int) string
}
