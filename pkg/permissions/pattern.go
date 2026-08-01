package permissions

import "strings"

// LiteralCommandPattern turns a concrete command into a rule pattern that
// matches the command itself and nothing else (kimi-code's literalRulePattern:
// the machine records what ran; breadth is only ever declared by humans
// writing globs by hand). Glob metacharacters are escaped as character
// classes because MatchCommand passes patterns through filepath.ToSlash,
// which would destroy backslash escapes.
func LiteralCommandPattern(command string) string {
	if strings.TrimSpace(command) == "" {
		return "*"
	}
	r := strings.NewReplacer(
		"[", "[[]",
		"*", "[*]",
		"?", "[?]",
	)
	return r.Replace(command)
}
