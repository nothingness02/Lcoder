package permissions

import "strings"

// PatternForCommand turns a concrete command into a glob pattern suitable for
// storing in a learned rule. It uses the first one or two tokens.
func PatternForCommand(command string) string {
	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return "*"
	}
	if len(tokens) >= 2 && !strings.HasPrefix(tokens[1], "-") {
		return tokens[0] + " " + tokens[1] + " *"
	}
	return tokens[0] + " *"
}
