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

// RequiredString returns args[key] as a string. It returns an error if the key
// is missing or not a string.
func RequiredString(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	if v == "" {
		return "", fmt.Errorf("required argument %q is empty", key)
	}
	return v, nil
}

// Bool returns args[key] as a bool, or defaultVal if missing / not a bool.
func Bool(args map[string]any, key string, defaultVal bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return defaultVal
}

// Float64 returns args[key] as a float64, accepting float64 / int / int64 / json.Number.
func Float64(args map[string]any, key string, defaultVal float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
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
