// pkg/llm/provider/cachepolicy_test.go
package provider

import "testing"

func TestCacheMarksDisabledWhenNone(t *testing.T) {
	marks := ComputeCacheMarks("none", []int{1}, 3, true)
	if marks.System || len(marks.MessageIdx) != 0 || marks.LastTool {
		t.Fatalf("expected no marks when cache=none, got %+v", marks)
	}
}

func TestCacheMarksDisabledWhenNotAnthropic(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{1}, 3, false)
	if marks.System || len(marks.MessageIdx) != 0 {
		t.Fatalf("expected no marks for non-anthropic, got %+v", marks)
	}
}

func TestCacheMarksUsesBreakpoints(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{0, 2}, 3, true)
	if !marks.System || !marks.LastTool {
		t.Fatalf("expected system+lastTool marks, got %+v", marks)
	}
	if len(marks.MessageIdx) != 2 || marks.MessageIdx[0] != 0 || marks.MessageIdx[1] != 2 {
		t.Fatalf("breakpoints wrong: %+v", marks.MessageIdx)
	}
}

func TestCacheMarksFallbackLastMsg(t *testing.T) {
	marks := ComputeCacheMarks("auto", nil, 4, true)
	if len(marks.MessageIdx) != 1 || marks.MessageIdx[0] != 3 {
		t.Fatalf("expected fallback to last index 3, got %+v", marks.MessageIdx)
	}
}

// TestCacheMarksRespectsBreakpointCap guards the hard Anthropic limit of 4
// cache_control blocks per request. System and the last tool definition always
// consume two, so at most two message breakpoints may survive; exceeding the cap
// makes the API reject the whole request.
func TestCacheMarksRespectsBreakpointCap(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{0, 2, 4, 6, 8}, 10, true)
	budget := maxCacheBreakpoints - 2 // system + last tool
	if len(marks.MessageIdx) > budget {
		t.Fatalf("expected at most %d message marks, got %+v", budget, marks.MessageIdx)
	}
	// The tail anchor is the one that matters for the next request's hit, so the
	// surviving marks must include the highest index.
	last := marks.MessageIdx[len(marks.MessageIdx)-1]
	if last != 8 {
		t.Fatalf("expected the tail breakpoint (8) to survive, got %+v", marks.MessageIdx)
	}
}

// TestCacheMarksDropsOutOfRange keeps stale indices off the wire: a breakpoint
// past the message count would silently mark nothing.
func TestCacheMarksDropsOutOfRange(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{1, 99, -3}, 3, true)
	if len(marks.MessageIdx) != 1 || marks.MessageIdx[0] != 1 {
		t.Fatalf("expected only index 1 to survive, got %+v", marks.MessageIdx)
	}
}
