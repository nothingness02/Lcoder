package permissions

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
	"github.com/lcoder/lcoder/internal/strutil"
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

type ruleSource struct {
	name   string
	rules  Config
	origin int // higher wins on tie
}

// Engine evaluates permission requests.
type Engine struct {
	sources    []ruleSource
	nextOrigin int
	unsafeMode bool
}

// NewEngine creates a permission engine from config.
func NewEngine(cfg Config) *Engine {
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]RuleTable)
	}
	e := &Engine{nextOrigin: 1}
	e.AddSource("config", cfg)
	return e
}

// SetUnsafeMode enables or disables the permission engine bypass.
func (e *Engine) SetUnsafeMode(v bool) {
	e.unsafeMode = v
}

// UnsafeMode returns the current unsafe mode state.
func (e *Engine) UnsafeMode() bool {
	return e.unsafeMode
}

// AddSource appends a rule source. Later sources win on specificity tie.
func (e *Engine) AddSource(name string, cfg Config) {
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]RuleTable)
	}
	e.sources = append(e.sources, ruleSource{name: name, rules: cfg, origin: e.nextOrigin})
	e.nextOrigin++
}

// LoadProjectRules loads rules from a project-local YAML file.
func (e *Engine) LoadProjectRules(path string) error {
	cfg, err := loadRulesYAML(path)
	if err != nil {
		return err
	}
	e.AddSource("project", cfg)
	return nil
}

// LoadGlobalLearnedRules loads rules from the global learned rules file.
func (e *Engine) LoadGlobalLearnedRules(path string) error {
	cfg, err := loadRulesYAML(path)
	if err != nil {
		return err
	}
	e.AddSource("global-learned", cfg)
	return nil
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

// dangerousTools default to Deny when no rule table exists, so that an
// omitted or empty permission config cannot silently allow destructive ops.
var dangerousTools = map[string]bool{
	"write": true,
	"edit":  true,
	"bash":  true,
}

var ultraDestructivePatterns = []string{
	"rm -rf /",
	"rm -rf /*",
	"rm -rf / *",
	"sudo *",
	"su *",
	"doas *",
	"mkfs.*",
	"fdisk *",
	"dd *",
	"reboot",
	"shutdown *",
	"halt",
	"poweroff",
	"systemctl *",
	"chmod -R 777 /",
	"chmod -R 777 /*",
	"chown -R root /",
	":(){ :|:& };:",
}

// IsUltraDestructive reports whether command matches the built-in ultra-
// destructive blacklist. This check is independent of configured rules.
func (e *Engine) IsUltraDestructive(command string) bool {
	return IsUltraDestructiveCommand(command)
}

// IsUltraDestructiveCommand reports whether command matches the built-in ultra-
// destructive blacklist. It is exposed as a package-level helper so callers
// without an Engine instance can reuse the same logic and pattern set.
func IsUltraDestructiveCommand(command string) bool {
	norm := strutil.CollapseSpace(command)
	if norm == "" {
		return false
	}
	for _, p := range ultraDestructivePatterns {
		if matched, _ := MatchUltraDestructive(p, norm); matched {
			return true
		}
	}
	return false
}

// MatchUltraDestructive matches a single ultra-destructive pattern against a
// normalized command. It treats '/' as a literal character.
func MatchUltraDestructive(pattern, target string) (bool, error) {
	const placeholder = "\x00"
	return path.Match(strings.ReplaceAll(pattern, "/", placeholder), strings.ReplaceAll(target, "/", placeholder))
}

// Evaluate returns the decision for a request.
// Unknown tools default to Allow; dangerous tools default to Deny unless a
// rule table (even an empty one) exists for them.
func (e *Engine) Evaluate(req Request) Decision {
	if e.unsafeMode {
		if req.Tool == "bash" && req.Command != "" && e.IsUltraDestructive(req.Command) {
			return Ask
		}
		return Allow
	}

	table, ok := e.mergedRules(req.Tool)
	if !ok {
		if dangerousTools[req.Tool] {
			return Deny
		}
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
		if dangerousTools[req.Tool] {
			return Deny
		}
		return Allow
	}
	return best
}

func (e *Engine) mergedRules(tool string) (RuleTable, bool) {
	merged := make(RuleTable)
	found := false
	for _, src := range e.sources {
		if table, ok := src.rules.Rules[tool]; ok {
			found = true
			for pattern, decision := range table {
				// Later sources overwrite earlier ones at same pattern.
				merged[pattern] = decision
			}
		}
	}
	return merged, found
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

func loadRulesYAML(path string) (Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Config{Rules: map[string]RuleTable{}}, nil
	}
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return Config{}, fmt.Errorf("load rules %s: %w", path, err)
	}
	var raw struct {
		Permissions struct {
			Rules map[string]map[string]string `yaml:"rules"`
		} `yaml:"permissions"`
	}
	if err := k.Unmarshal("", &raw); err != nil {
		return Config{}, err
	}
	cfg := Config{Rules: make(map[string]RuleTable)}
	for tool, table := range raw.Permissions.Rules {
		rt := make(RuleTable)
		for pattern, decision := range table {
			rt[pattern] = Decision(decision)
		}
		cfg.Rules[tool] = rt
	}
	return cfg, nil
}
