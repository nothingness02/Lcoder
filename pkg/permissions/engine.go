package permissions

import (
	"fmt"
	"os"
	"sync"

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

// Engine evaluates permission requests through an ordered policy chain (see
// policy.go). User rule sources feed the rule policies; guard policies
// installed via SetGuardPolicies run ahead of the built-in chain.
type Engine struct {
	sources    []ruleSource
	nextOrigin int
	unsafeMode bool
	guards     []Policy

	// cwd/homeDir give the engine the session's path context so path rules
	// can be matched against equivalent spellings of a path (see
	// pathVariants). Empty means pure lexical matching (tests, headless use).
	cwd     string
	homeDir string

	sessionMu    sync.RWMutex
	sessionRules map[string]map[string]bool // tool -> exact target -> approved
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

// SetGuardPolicies replaces the guard policies that run ahead of the
// built-in chain. Guards express harness constraints (e.g. mode/skill tool
// surface restrictions) that must hold regardless of unsafe mode or user
// rules, so they are always evaluated first.
func (e *Engine) SetGuardPolicies(policies ...Policy) {
	e.guards = policies
}

// SetPathContext gives the engine the session's working directory and home
// directory, enabling path-variant matching: rules then match paths the way
// the tools will resolve them, not just the raw string the model wrote.
// Callers should wire the process cwd and os.UserHomeDir here.
func (e *Engine) SetPathContext(cwd, homeDir string) {
	e.cwd = cwd
	e.homeDir = homeDir
}

// AddSessionRule records an exact-match approval for the tool call that
// lasts for the rest of the session (in memory only — it is never persisted,
// so it can never leak into other sessions). Commands are matched verbatim;
// path targets are stored in canonical form (when path context is set), so
// approving "/repo/a.go" also covers a later "a.go" spelling of the same
// file — but never a different file.
func (e *Engine) AddSessionRule(tool string, args map[string]any) {
	target := e.sessionTarget(RequestFor(tool, args))
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.sessionRules == nil {
		e.sessionRules = make(map[string]map[string]bool)
	}
	if e.sessionRules[tool] == nil {
		e.sessionRules[tool] = make(map[string]bool)
	}
	e.sessionRules[tool][target] = true
}

// hasSessionRule reports whether the exact (tool, target) pair was approved
// earlier in this session.
func (e *Engine) hasSessionRule(req Request) bool {
	target := e.sessionTarget(req)
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	return e.sessionRules[req.Tool][target]
}

// sessionTarget normalizes the match target for session rules: commands
// stay verbatim; path targets are canonicalized when possible so equivalent
// spellings of one file share a single approval.
func (e *Engine) sessionTarget(req Request) string {
	target := requestTarget(req)
	if req.Command == "" {
		if c := e.canonicalPath(target); c != "" {
			target = c
		}
	}
	return target
}

// ClearSessionRules drops all session approvals (e.g. on session reset).
func (e *Engine) ClearSessionRules() {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	e.sessionRules = nil
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

// RequestFor builds a Request from a tool name and its call arguments,
// lifting the well-known path/command arguments into dedicated fields.
func RequestFor(tool string, args map[string]any) Request {
	req := Request{Tool: tool, Args: args}
	if path, ok := args["path"].(string); ok {
		req.Path = path
	}
	if cmd, ok := args["command"].(string); ok {
		req.Command = cmd
	}
	return req
}

// Decide returns the decision for a tool call using the command/path target.
func (e *Engine) Decide(tool string, args map[string]any) Decision {
	return e.Evaluate(RequestFor(tool, args))
}

// DecideWithSource is Decide plus the name of the deciding policy and its
// reason (see EvaluateWithSource).
func (e *Engine) DecideWithSource(tool string, args map[string]any) (Decision, string, string) {
	return e.EvaluateWithSource(RequestFor(tool, args))
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
	return MatchCommand(pattern, target), nil
}

// Evaluate returns the decision for a request.
func (e *Engine) Evaluate(req Request) Decision {
	d, _, _ := e.EvaluateWithSource(req)
	return d
}

// EvaluateWithSource returns the decision along with the name of the policy
// that made it and the policy-provided reason (empty when the policy gave
// none). The reason is meant to be fed back to the model.
func (e *Engine) EvaluateWithSource(req Request) (decision Decision, policy, reason string) {
	for _, p := range e.chain() {
		if d, why, ok := p.Decide(req); ok {
			return d, p.Name(), why
		}
	}
	// Unreachable while fallbackAllowPolicy terminates the chain.
	return Allow, "default-allow", ""
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

// DefaultConfig returns the default allow-all permission config.
func DefaultConfig() Config {
	return Config{Rules: map[string]RuleTable{}}
}

// Explain returns a human-readable explanation for a decision, naming the
// policy that made it.
func (e *Engine) Explain(req Request) string {
	decision, policy, reason := e.EvaluateWithSource(req)
	if reason != "" {
		return fmt.Sprintf("%s: %s (%s: %s)", decision, req.Tool, policy, reason)
	}
	return fmt.Sprintf("%s: %s (by %s)", decision, req.Tool, policy)
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
