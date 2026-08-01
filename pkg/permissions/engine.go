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

// ruleStore holds the ordered rule sources shared by an engine and its
// forks. Learned rules (LoadProjectRules / LoadGlobalLearnedRules) land here
// so every in-process agent sees them immediately.
type ruleStore struct {
	mu         sync.RWMutex
	sources    []ruleSource
	nextOrigin int
}

// SessionApprovalStore holds exact-match approvals shared by an engine and
// its forks: an approval one agent grants is honored by every agent in the
// process — it is the same trust conversation with the same user.
type SessionApprovalStore struct {
	mu    sync.RWMutex
	rules map[string]map[string]bool // tool -> exact target -> approved
}

func (s *SessionApprovalStore) add(tool, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules == nil {
		s.rules = make(map[string]map[string]bool)
	}
	if s.rules[tool] == nil {
		s.rules[tool] = make(map[string]bool)
	}
	s.rules[tool][target] = true
}

func (s *SessionApprovalStore) has(tool, target string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules[tool][target]
}

func (s *SessionApprovalStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = nil
}

// Engine evaluates permission requests through an ordered policy chain (see
// policy.go). Rule sources and session approvals live in shared stores;
// guard policies and unsafe mode are per-instance, so a Forked engine can
// carry a different harness surface (mode/skill) over the same rules.
type Engine struct {
	rules   *ruleStore
	session *SessionApprovalStore

	unsafeMode bool
	guardsMu   sync.RWMutex
	guards     []Policy

	// hookPolicies are extension-provided and run mid-chain (see
	// SetHookPolicies for the position contract).
	hookPolicies []Policy

	// cwd/homeDir give the engine the session's path context so path rules
	// can be matched against equivalent spellings of a path (see
	// pathVariants). Empty means pure lexical matching (tests, headless use).
	cwd     string
	homeDir string
}

// NewEngine creates a permission engine from config.
func NewEngine(cfg Config) *Engine {
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]RuleTable)
	}
	e := &Engine{
		rules:   &ruleStore{nextOrigin: 1},
		session: &SessionApprovalStore{},
	}
	e.AddSource("config", cfg)
	return e
}

// Fork returns a child engine that shares rule sources and session approvals
// with its parent but owns its guard policies. In-process subagents each get
// a fork: the child's executor installs its own mode/skill guards without
// clobbering the parent's, while rule learning and session approvals stay
// visible process-wide.
func (e *Engine) Fork() *Engine {
	return &Engine{
		rules:        e.rules,
		session:      e.session,
		unsafeMode:   e.unsafeMode,
		hookPolicies: e.hookPolicySnapshot(),
		cwd:          e.cwd,
		homeDir:      e.homeDir,
	}
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
	e.guardsMu.Lock()
	defer e.guardsMu.Unlock()
	e.guards = policies
}

// SetHookPolicies replaces the extension-provided policies. They run AFTER
// deny rules and session approvals but before static user rules: deny stays
// absolute over extensions, and a user's explicit in-session approval cannot
// be vetoed by an extension, while extensions still outrank convenience
// rules (e.g. organization policy beating local allows).
func (e *Engine) SetHookPolicies(policies ...Policy) {
	e.guardsMu.Lock()
	defer e.guardsMu.Unlock()
	e.hookPolicies = policies
}

// guardPolicies returns a snapshot of the installed guard policies.
func (e *Engine) guardPolicies() []Policy {
	e.guardsMu.RLock()
	defer e.guardsMu.RUnlock()
	return append([]Policy(nil), e.guards...)
}

// hookPolicySnapshot returns a snapshot of the installed hook policies.
func (e *Engine) hookPolicySnapshot() []Policy {
	e.guardsMu.RLock()
	defer e.guardsMu.RUnlock()
	return append([]Policy(nil), e.hookPolicies...)
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
	e.session.add(tool, e.sessionTarget(RequestFor(tool, args)))
}

// hasSessionRule reports whether the exact (tool, target) pair was approved
// earlier in this session.
func (e *Engine) hasSessionRule(req Request) bool {
	return e.session.has(req.Tool, e.sessionTarget(req))
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
	e.session.clear()
}

// AddSource appends a rule source, replacing any earlier source with the
// same name (learned-rule reloads must not accumulate full copies). Later
// sources win on specificity tie.
func (e *Engine) AddSource(name string, cfg Config) {
	if cfg.Rules == nil {
		cfg.Rules = make(map[string]RuleTable)
	}
	e.rules.mu.Lock()
	defer e.rules.mu.Unlock()
	for i, src := range e.rules.sources {
		if src.name == name {
			e.rules.sources[i].rules = cfg
			return
		}
	}
	e.rules.sources = append(e.rules.sources, ruleSource{name: name, rules: cfg, origin: e.rules.nextOrigin})
	e.rules.nextOrigin++
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
	e.rules.mu.RLock()
	defer e.rules.mu.RUnlock()
	merged := make(RuleTable)
	found := false
	for _, src := range e.rules.sources {
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
