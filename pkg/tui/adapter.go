package tui

import (
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// toComponent converts an internal data block into a renderable component.
func toComponent(b block) components.BlockComponent {
	switch b.kind {
	case components.BlockSystem:
		return components.NewSystemLogComponent(b.id, b.raw)
	case components.BlockBanner:
		return components.NewBannerComponent(b.id, b.raw)
	case components.BlockUser:
		return components.NewUserComponent(b.id, b.raw, b.attachments)
	case components.BlockAssistant:
		var usage *components.UsageInfo
		if b.usage != nil {
			usage = &components.UsageInfo{
				InputTokens:  b.usage.inputTokens,
				OutputTokens: b.usage.outputTokens,
				TotalTokens:  b.usage.totalTokens,
				Cost:         b.usage.cost,
			}
		}
		comp := components.NewAssistantComponent(b.id, b.thinking, b.raw, usage)
		comp.SetExpanded(b.expanded)
		comp.SetThinkingSecs(b.thinkingSecs)
		return comp
	case components.BlockTool:
		comp := components.NewToolResultComponent(
			b.id,
			b.toolName,
			b.toolArgs,
			b.toolResult,
			b.toolErr,
			b.toolRunning,
			b.toolStart,
			b.elapsed,
		)
		comp.SetExpanded(b.expanded)
		comp.SetSubagentActivity(b.subagentLines, b.subagentTail, b.subagentLive)
		comp.SetSubagentChildren(subagentChildRows(b))
		comp.SetChip(b.toolChip)
		comp.SetToolDetails(b.toolDetails)
		return comp
	}
	return components.NewSystemLogComponent(b.id, b.raw)
}

// componentsFromBlocks converts a slice of blocks in order.
func componentsFromBlocks(blocks []block) []components.BlockComponent {
	out := make([]components.BlockComponent, len(blocks))
	for i, b := range blocks {
		out[i] = toComponent(b)
	}
	return out
}

// subagentChildRows converts a block's per-child state into component rows,
// preserving spawn order.
func subagentChildRows(b block) []components.SubagentChildRow {
	if len(b.subagentOrder) == 0 {
		return nil
	}
	rows := make([]components.SubagentChildRow, 0, len(b.subagentOrder))
	for _, id := range b.subagentOrder {
		child := b.subagentChildren[id]
		rows = append(rows, components.SubagentChildRow{
			Profile: child.profile,
			Status:  child.status,
			Tools:   child.tools,
			Started: child.started,
			Elapsed: child.elapsed,
		})
	}
	return rows
}
