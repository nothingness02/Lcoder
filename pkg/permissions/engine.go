package permissions

import (
	"fmt"
	"path/filepath"
)

// Decision is the result of a permission evaluation.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// Request describes an action the agent wants to perform.
type Request struct {
	Tool    string
	Args    map[string]any
	Path    string
	Command string
}

// RuleTable maps glob patterns to decisions for one tool.
type RuleTable map[string]Decision

// Config is the permission configuration loaded from lcoder.yaml.
type Config struct {
	Rules map[string]RuleTable // tool name -> pattern -> decision
}

// Rule is a single permission rule.
type Rule struct {
	Tool     string
	Pattern  string
	Decision Decision
}

// Engine evaluates permission requests.
type Engine struct {
	cfg Config
}

// NewEngine creates a permission engine from config.
func NewEngine(cfg Config) *Engine {
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]RuleTable)
	}
	return &Engine{cfg: cfg}
}

// NewEngineFromRules creates a permission engine from a slice of rules.
func NewEngineFromRules(rules []Rule) *Engine {
	rulesMap := make(map[string]RuleTable)
	for _, r := range rules {
		if _, ok := rulesMap[r.Tool]; !ok {
			rulesMap[r.Tool] = make(RuleTable)
		}
		rulesMap[r.Tool][r.Pattern] = r.Decision
	}
	return NewEngine(Config{Rules: rulesMap})
}

// Decide returns the decision for a tool call using the command/path target.
func (e *Engine) Decide(tool string, args map[string]any) Decision {
	req := Request{Tool: tool, Args: args}
	if path, ok := args["path"].(string); ok {
		req.Path = path
	}
	if cmd, ok := args["command"].(string); ok {
		req.Command = cmd
	}
	return e.Evaluate(req)
}

// Evaluate returns the decision for a request.
// Default is Allow unless explicitly configured otherwise.
func (e *Engine) Evaluate(req Request) Decision {
	table, ok := e.cfg.Rules[req.Tool]
	if !ok {
		return Allow
	}

	target := req.Command
	if target == "" {
		target = req.Path
	}
	if target == "" {
		target = "*"
	}

	var bestPattern string
	var best Decision
	set := false

	for pattern, decision := range table {
		matched, err := match(pattern, target)
		if err != nil {
			continue
		}
		if matched {
			if !set || specificity(pattern) > specificity(bestPattern) {
				bestPattern = pattern
				best = decision
				set = true
			}
		}
	}
	if !set {
		return Allow
	}
	return best
}

// match checks whether a glob pattern matches a target.
func match(pattern, target string) (bool, error) {
	// filepath.Match supports * and ? but not **.
	return filepath.Match(pattern, target)
}

// specificity ranks a pattern by its length and number of literals.
func specificity(pattern string) int {
	return len(pattern)
}

// DefaultConfig returns the default allow-all permission config.
func DefaultConfig() Config {
	return Config{Rules: map[string]RuleTable{}}
}

// Explain returns a human-readable explanation for a decision.
func (e *Engine) Explain(req Request) string {
	decision := e.Evaluate(req)
	switch decision {
	case Allow:
		return fmt.Sprintf("allowed: %s", req.Tool)
	case Ask:
		return fmt.Sprintf("requires approval: %s", req.Tool)
	case Deny:
		return fmt.Sprintf("denied by policy: %s", req.Tool)
	default:
		return fmt.Sprintf("unknown decision for %s", req.Tool)
	}
}

