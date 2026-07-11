package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// ValidateArgs checks args against a tool's JSON-Schema parameters. It verifies
// that required fields are present and that the top-level type of each provided
// field matches its declared JSON type. It returns nil when args are valid, or
// an LLM-friendly error describing the first problem found. Schemas that are not
// a recognizable object schema (no "properties") pass through unchecked.
func ValidateArgs(def models.ToolDefinition, args map[string]any) error {
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		return nil // not a recognizable object schema — degrade gracefully
	}
	// required fileds
	for _, name := range requiredFields(def.Parameters) {
		if _, present := args[name]; !present {
			return fmt.Errorf("invalid arguments for %q: missing required field %q%s",
				def.Name, name, expectedSuffix(props, name, args))
		}
	}
	// type match
	for name, value := range args {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue // field not described by schema — nothing to check
		}
		wantType, ok := spec["type"].(string)
		if !ok {
			continue // schema declares no type for this field
		}
		if !typeMatches(wantType, value) {
			return fmt.Errorf("invalid arguments for %q: field %q must be %s, got %s",
				def.Name, name, wantType, jsonTypeOf(value))
		}
	}
	return nil
}

// typeMatches reports whether value's Go type satisfies the JSON type name.
// Numbers are accepted leniently because providers deserialize them
// inconsistently (float64, int, int64, or json.Number).
func typeMatches(jsonType string, value any) bool {
	switch jsonType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number", "integer":
		switch value.(type) {
		case float64, int, int64, json.Number:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	}
	return true // unknown JSON type — do not block
}

// jsonTypeOf names value's apparent JSON type for error messages.
func jsonTypeOf(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case float64, int, int64, json.Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// requiredFields extracts the "required" list, tolerating both []string and
// []any (the latter is what JSON unmarshaling produces).
func requiredFields(params map[string]any) []string {
	switch r := params["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// expectedSuffix renders " (expected <type>); provided: a, b" to help the LLM
// self-correct. The type fragment is omitted when the field declares none.
func expectedSuffix(props map[string]any, name string, args map[string]any) string {
	var b strings.Builder
	if spec, ok := props[name].(map[string]any); ok {
		if t, ok := spec["type"].(string); ok {
			fmt.Fprintf(&b, " (expected %s)", t)
		}
	}
	provided := make([]string, 0, len(args))
	for k := range args {
		provided = append(provided, k)
	}
	if len(provided) > 0 {
		fmt.Fprintf(&b, "; provided: %s", strings.Join(provided, ", "))
	}
	return b.String()
}
