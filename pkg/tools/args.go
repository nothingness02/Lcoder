package tools

import (
	"encoding/json"
	"fmt"
)

// String returns args[key] as a string, or defaultVal if missing / not a string.
func String(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return defaultVal
}

// Int returns args[key] as an int, accepting float64 / int / int64 / json.Number.
func Int(args map[string]any, key string, defaultVal int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	}
	return defaultVal
}

// StringSlice returns args[key] as []string, accepting []any or []string.
func StringSlice(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	raw := args[key]
	if raw == nil {
		return nil
	}
	if ss, ok := raw.([]string); ok {
		return ss
	}
	if aa, ok := raw.([]any); ok {
		out := make([]string, 0, len(aa))
		for _, v := range aa {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// NormalizeArgs returns a stable string key for a map of tool arguments.
// It uses JSON marshal, which sorts map keys deterministically.
func NormalizeArgs(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		// Fallback: keys joined. Should never happen for valid args.
		return fmt.Sprintf("%v", args)
	}
	return string(b)
}
