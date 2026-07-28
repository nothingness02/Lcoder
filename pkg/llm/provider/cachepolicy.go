// pkg/llm/provider/cachepolicy.go
package provider

// CacheMarks describes where Anthropic ephemeral cache_control should be applied
// for a turn. Computed by the engine, consumed by the Anthropic adapter.
type CacheMarks struct {
	System     bool  // mark the system block cacheable
	LastTool   bool  // mark the last tool definition cacheable
	MessageIdx []int // message indices whose first text block is cacheable
}

// maxCacheBreakpoints is the hard Anthropic limit on cache_control blocks per
// request. Exceeding it makes the API reject the whole request, so marks are
// truncated rather than passed through.
const maxCacheBreakpoints = 4

// ComputeCacheMarks ports apply_cache_policy: Anthropic-only ephemeral caching.
// cache=="none" or a non-Anthropic provider disables it. Explicit breakpoints
// are used as-is; otherwise it falls back to the last message so at least one
// breakpoint exists.
func ComputeCacheMarks(cache string, breakpoints []int, msgCount int, anthropic bool) CacheMarks {
	if cache == "none" || !anthropic {
		return CacheMarks{}
	}
	m := CacheMarks{System: true, LastTool: true}
	if len(breakpoints) > 0 {
		for _, b := range breakpoints {
			if b >= 0 && b < msgCount {
				m.MessageIdx = append(m.MessageIdx, b)
			}
		}
		// System and the last tool definition each consume one slot, leaving this
		// many for the message array. When callers over-mark, keep the newest
		// anchors: the tail is what the next request must hit, while the static
		// prefix is already covered by the system and tool marks.
		if budget := maxCacheBreakpoints - 2; len(m.MessageIdx) > budget {
			m.MessageIdx = m.MessageIdx[len(m.MessageIdx)-budget:]
		}
	} else if msgCount > 0 {
		m.MessageIdx = []int{msgCount - 1}
	}
	return m
}
