package components

import "github.com/charmbracelet/lipgloss"

// BannerComponent renders a pre-rendered ANSI block verbatim (the startup brand
// banner committed into the scrollback) with no additional styling, so embedded
// colors survive.
type BannerComponent struct {
	id  string
	raw string
}

func NewBannerComponent(id, raw string) *BannerComponent {
	return &BannerComponent{id: id, raw: raw}
}

func (c *BannerComponent) ID() string      { return c.id }
func (c *BannerComponent) Kind() BlockKind { return BlockBanner }

func (c *BannerComponent) Height(width int, expanded bool) int {
	return lipgloss.Height(c.raw)
}

func (c *BannerComponent) Render(width int, expanded bool) string {
	return c.raw
}
