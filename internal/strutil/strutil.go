// Package strutil provides small string helpers used across Lcoder.
package strutil

import "strings"

// ContainsAny reports whether s contains any of the given substrings.
func ContainsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// CollapseSpace trims leading/trailing whitespace and collapses any run of
// whitespace to a single space.
func CollapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
