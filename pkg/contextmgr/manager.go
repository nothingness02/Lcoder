package contextmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)

// TokenBudget defines hard and soft limits for context sizing.
type TokenBudget struct {
	MaxTotal      int // Hard upper bound (model context window)
	TargetTotal   int // Desired upper bound
	ReserveOutput int // Tokens reserved for model output
	// MaxOutput is the resolved single-response output ceiling: the smaller of
	// the model's official ceiling and any explicit user cap. 0 means unknown,
	// in which case ResolveMaxTokens falls back to a conservative default.
	MaxOutput int
	// DropThreshold is the ratio of MaxTotal at which old messages are dropped.
	// Zero defaults to 1.0 (drop only when exceeding max).
	DropThreshold float64
	// StaticRatio caps static/stable blocks at this percentage of the effective
	// input window. Zero or >=100 disables the cap.
	StaticRatio int
}

// budgetFallbackOutput caps a single response when MaxOutput is unknown.
const budgetFallbackOutput = 16384

// budgetMinOutput is the floor for a resolved output cap: even when the context
// window is nearly full, always leave room to emit at least a small tool call
// rather than starving the response to zero.
const budgetMinOutput = 1024

// ResolveMaxTokens returns the effective max_tokens for one request, given the
// estimated input token count already assembled for the turn. It is the minimum
// of the model's output ceiling (MaxOutput, or a fallback when unknown) and the
// context window left after the input (MaxTotal - inputTokens), floored so a
// nearly-full window never drives the cap to zero.
func (b TokenBudget) ResolveMaxTokens(inputTokens int) int {
	out := b.MaxOutput
	if out <= 0 {
		out = budgetFallbackOutput
	}
	if b.MaxTotal > 0 {
		if remaining := b.MaxTotal - inputTokens; remaining < out {
			out = remaining
		}
	}
	if out < budgetMinOutput {
		out = budgetMinOutput
	}
	return out
}

// EffectiveInput returns the budget left for input after reserving output.
func (b TokenBudget) EffectiveInput() int {
	return b.MaxTotal - b.ReserveOutput
}

// DropLimit returns the token count at which old messages should be dropped.
func (b TokenBudget) DropLimit() int {
	thr := b.DropThreshold
	if thr <= 0 {
		thr = 1.0
	}
	return int(float64(b.MaxTotal-b.ReserveOutput) * thr)
}

// TokenEstimator estimates token count for a slice of messages.
type TokenEstimator func(messages []models.AgentMessage) int

// SummarizeFunc generates a summary from messages. The context carries run
// cancellation; summarizers must honor it.
//
// prior is the previous compaction's summary text, or empty on the first fold.
// foldOlder extracts it from the span being folded and passes it here instead of
// leaving it inline, so a repeated fold merges into the earlier summary rather
// than summarizing it again.
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error)

// CompactionSink durably records a committed fold. It is called by foldOlder
// immediately after the fold is committed to the live context, while still
// holding the same call — so "the context was folded" and "the fold was
// recorded" cannot diverge.
//
// This is deliberately a callback rather than an event subscription. The fold is
// destructive in memory: the older span is gone once ReplaceRecent runs. If the
// durable record is missed, the session's compacted view still claims those
// messages are active and a resume replays them, undoing the pressure the fold
// relieved. An event subscription makes that a delivery concern spread across
// packages; a sink makes it one branch next to the code that does the folding.
//
// A sink error is reported to the caller of MaybeCompactLeveled but does not
// roll back the in-memory fold: the context is already smaller, and re-inflating
// it would risk overflowing the window that the fold was relieving.
type CompactionSink func(FoldResult, []models.AgentMessage) error

// Manager manages structured context blocks within a token budget.
type Manager struct {
	mu     sync.Mutex
	budget TokenBudget
	blocks []*Block
	estimator  TokenEstimator
	summarizer SummarizeFunc
	policy     WindowPolicy
	keepRecent int
	// keepRecentTokens is the token budget for the kept tail at proactive
	// pressure; tighter levels derive from it in keepTokensForLevel.
	keepRecentTokens int

	// ephemeralReminders are injected into the next BuildTurnRequest only and
	// never persisted to a block — see ephemeral.go.
	ephemeralReminders []string

	// lastUsage / hasUsage hold provider-reported real prompt-token accounting
	// from the most recent turn — see usage.go.
	lastUsage RealUsage
	hasUsage  bool

	// cachePolicy controls how aggressively cache breakpoints are placed; empty
	// means CachePolicyDefault.
	cachePolicy CacheHintPolicy

	// thinking is the resolved thinking value carried on turn requests.
	thinking string

	// sink durably records committed folds; nil means folds are not persisted.
	sink CompactionSink
}

// Option configures a Manager.
type Option func(*Manager)

// WithEstimator sets a custom token estimator.
func WithEstimator(e TokenEstimator) Option {
	return func(m *Manager) { m.estimator = e }
}

// WithSummarizer sets the summarizer used for compaction.
func WithSummarizer(s SummarizeFunc) Option {
	return func(m *Manager) { m.summarizer = s }
}

// WithWindowPolicy sets the window policy.
func WithWindowPolicy(p WindowPolicy) Option {
	return func(m *Manager) { m.policy = p }
}

// WithMinRecent sets the minimum number of recent messages MaybeCompact retains
// (alongside the last user message) when folding older messages into a summary.
func WithMinRecent(n int) Option {
	return func(m *Manager) {
		if n < 1 {
			n = 1
		}
		m.keepRecent = n
	}
}

// WithKeepRecentTokens sets the token budget for the kept tail. Zero or
// negative falls back to the default of 20000 (pi's keepRecentTokens).
func WithKeepRecentTokens(n int) Option {
	return func(m *Manager) {
		if n <= 0 {
			n = defaultKeepRecentTokens
		}
		m.keepRecentTokens = n
	}
}

// WithCacheHintPolicy sets the cache breakpoint policy for BuildTurnRequest.
func WithCacheHintPolicy(p CacheHintPolicy) Option {
	return func(m *Manager) { m.cachePolicy = p }
}

// WithThinking sets the resolved thinking value carried on turn requests.
func WithThinking(v string) Option {
	return func(m *Manager) { m.thinking = v }
}

// SetThinking replaces the resolved thinking value carried on turn requests.
// Intended for runtime adjustment from the TUI; the value must already be
// validated by engine.ResolveThinking before it reaches here.
func (m *Manager) SetThinking(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thinking = v
}

// Thinking returns the current resolved thinking value ("" = send nothing).
func (m *Manager) Thinking() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.thinking
}

// WithCompactionSink sets the durable recorder for committed folds.
func WithCompactionSink(s CompactionSink) Option {
	return func(m *Manager) { m.sink = s }
}

// NewManager creates a context manager with the given budget.
func NewManager(budget TokenBudget, opts ...Option) *Manager {
	m := &Manager{
		budget:           budget,
		estimator:        DefaultEstimator,
		summarizer:       nil,
		policy:           &KeepRecentInBudget{},
		keepRecent:       10,
		keepRecentTokens: defaultKeepRecentTokens,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// SetBudget replaces the manager's token budget in place. Blocks and history are
// untouched, so a live model switch can re-size the budget without losing the
// conversation.
func (m *Manager) SetBudget(b TokenBudget) {
	m.budget = b
}

// Budget returns the manager's current token budget.
func (m *Manager) Budget() TokenBudget {
	return m.budget
}

// Estimator returns the manager's token estimator.
func (m *Manager) Estimator() TokenEstimator {
	return m.estimator
}

// Summarizer returns the manager's summarizer.
func (m *Manager) Summarizer() SummarizeFunc {
	return m.summarizer
}

// SetSummarizer replaces the summarizer used for compaction. Intended for
// startup wiring while the manager is idle.
func (m *Manager) SetSummarizer(s SummarizeFunc) {
	m.summarizer = s
}

// WindowPolicy returns the manager's window policy.
func (m *Manager) WindowPolicy() WindowPolicy {
	return m.policy
}

// SetBlock replaces an existing block of the same kind and name, or appends it.
func (m *Manager) SetBlock(block *Block) {
	for i, b := range m.blocks {
		if b.Kind == block.Kind && b.Name == block.Name {
			m.blocks[i] = block
			return
		}
	}
	m.blocks = append(m.blocks, block)
}

// GetBlock returns the first block matching kind and name.
func (m *Manager) GetBlock(kind BlockKind, name string) (*Block, bool) {
	for _, b := range m.blocks {
		if b.Kind == kind && b.Name == name {
			return b, true
		}
	}
	return nil, false
}

// AppendRecent appends a message to the recent messages block.
func (m *Manager) AppendRecent(msg models.AgentMessage) {
	b, ok := m.GetBlock(BlockRecent, "recent")
	if !ok {
		b = NewBlock(BlockRecent, "recent", StabilityDynamic, 100)
		m.SetBlock(b)
	}
	b.Messages = append(b.Messages, msg)
}

// SetBlockWithTurn replaces a block and records the current turn for cache decisions.
func (m *Manager) SetBlockWithTurn(block *Block, turn int) {
	block.LastModifiedTurn = turn
	m.SetBlock(block)
}

// RemoveBlock removes the first block matching kind and name.
func (m *Manager) RemoveBlock(kind BlockKind, name string) {
	for i, b := range m.blocks {
		if b.Kind == kind && b.Name == name {
			m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
			return
		}
	}
}

// Blocks returns blocks in canonical order.
func (m *Manager) Blocks() []*Block {
	order := DefaultBlockOrder()
	orderIndex := make(map[BlockKind]int, len(order))
	for i, k := range order {
		orderIndex[k] = i
	}

	ordered := make([]*Block, len(m.blocks))
	copy(ordered, m.blocks)
	// Stable sort by canonical order, then by priority descending.
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			ai := orderIndex[ordered[i].Kind]
			aj := orderIndex[ordered[j].Kind]
			if ai > aj || (ai == aj && ordered[i].Priority < ordered[j].Priority) {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	return ordered
}

// EstimateTokens returns the estimated token count for messages.
func (m *Manager) EstimateTokens(messages []models.AgentMessage) int {
	return m.estimator(messages)
}

// BuildTurnRequest selects blocks within budget and builds a TurnRequest.
// It also computes cache breakpoints based on block boundaries and stability.
func (m *Manager) BuildTurnRequest(model models.ModelRef, tools []models.ToolDefinition) (models.TurnRequest, error) {
	blocks, err := m.policy.Apply(m.Blocks(), m.budget, m)
	if err != nil {
		return models.TurnRequest{}, fmt.Errorf("apply window policy: %w", err)
	}

	var systemParts []string
	var messages []models.AgentMessage
	var breakpoints []int
	var messageIdx int
	var stableTokens int

	policy := m.cachePolicy
	if policy == "" {
		policy = CachePolicyDefault
	}
	prefixThreshold := 256
	switch policy {
	case CachePolicyAggressive:
		prefixThreshold = 1
	case CachePolicyNone:
		prefixThreshold = 1 << 30
	}

	for _, b := range blocks {
		if IsSystemBlock(b) {
			systemParts = append(systemParts, b.Text())
			stableTokens += m.EstimateTokens(b.Messages)
			continue
		}
		if len(b.Messages) == 0 {
			continue
		}
		// Blocks explicitly hinted not worth caching are skipped for breakpoints.
		if b.CacheHint == CacheHintSkip {
			messages = append(messages, b.Messages...)
			messageIdx += len(b.Messages)
			continue
		}
		// Place a cache breakpoint at the first non-system message when the
		// prefix (system/static/stable blocks) is large enough to be worth caching.
		if messageIdx == 0 && stableTokens >= prefixThreshold && policy != CachePolicyNone {
			breakpoints = append(breakpoints, messageIdx)
		}
		// Explicit block-level hints also produce breakpoints.
		if b.CacheHint == CacheHintBreakpoint && policy != CachePolicyNone {
			breakpoints = append(breakpoints, messageIdx)
		}
		messages = append(messages, b.Messages...)
		messageIdx += len(b.Messages)
	}

	// Anchor the tail on the last stable message so everything accumulated during
	// the turn — the model's tool calls and their results — lands inside the
	// cached prefix. Anchoring on the last *user* message instead would leave the
	// growing tool_use/tool_result tail outside the cache, re-billing it as fresh
	// input on every step of a tool loop.
	//
	// Computed BEFORE injecting ephemeral reminders: ephemeral content changes
	// every turn, so anchoring on it would bust the cached prefix each request.
	if policy != CachePolicyNone && len(messages) > 0 {
		breakpoints = append(breakpoints, len(messages)-1)
	}

	// Anthropic caps a request at 4 cache_control blocks, so duplicate or
	// unordered indices waste that budget and can push a valid request over the
	// limit. Normalize before handing them to the provider.
	breakpoints = normalizeBreakpoints(breakpoints)

	// Inject ephemeral system-reminders as a trailing synthetic user message.
	// They live only in this request: never stored in any block, so they vanish
	// next turn unless re-set (mirrors Claude Code's <system-reminder> blocks).
	if em, ok := m.buildEphemeralMessage(); ok {
		messages = append(messages, em)
	}

	// Cap output at min(model ceiling, remaining window). Input is the assembled
	// system prompt plus all messages actually sent this turn, so the estimate
	// reflects exactly what the provider will bill as input.
	systemPrompt := strings.Join(systemParts, "\n\n")
	inputTokens := stableTokens + m.EstimateTokens(messages)

	// cache_hint_policy: none must reach the provider. Without it the adapter
	// still marks the system block, the last tool definition, and a fallback
	// tail message, so the policy would only suppress the breakpoints computed
	// here and caching would stay on.
	cacheMode := "auto"
	if policy == CachePolicyNone {
		cacheMode = "none"
	}

	return models.TurnRequest{
		Model:            model,
		SystemPrompt:     systemPrompt,
		Messages:         messages,
		Tools:            tools,
		Cache:            cacheMode,
		CacheBreakpoints: breakpoints,
		Thinking:         m.thinking,
		Generation:       models.GenerationConfig{MaxTokens: m.budget.ResolveMaxTokens(inputTokens)},
	}, nil
}

// AllMessages returns all messages across all blocks in canonical order.
func (m *Manager) AllMessages() []models.AgentMessage {
	var messages []models.AgentMessage
	for _, b := range m.Blocks() {
		if !IsSystemBlock(b) {
			messages = append(messages, b.Messages...)
		}
	}
	return messages
}

// ReplaceRecent replaces the recent messages block with the given messages.
func (m *Manager) ReplaceRecent(msgs []models.AgentMessage) {
	m.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, msgs...))
}

// MaybeCompact is the legacy threshold-based compaction entry point. It now
// delegates to MaybeCompactLeveled so only one compaction policy remains.
func (m *Manager) MaybeCompact() (bool, error) {
	_, res, err := m.MaybeCompactLeveled(context.Background())
	return res.Committed, err
}

// ClearRecent removes all messages from the recent block.
func (m *Manager) ClearRecent() {
	m.ReplaceRecent(nil)
}

// SystemPrompt returns the merged system prompt from all system blocks.
func (m *Manager) SystemPrompt() string {
	var parts []string
	for _, b := range m.Blocks() {
		if IsSystemBlock(b) {
			if text := b.Text(); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// SetSystemPrompt sets the primary system prompt block.
func (m *Manager) SetSystemPrompt(text string) {
	m.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text})))
}

// SetMessages rebuilds the conversation from a flat message list. Genuine system
// messages become the system prompt; compacted summaries (metadata compacted=true)
// stay in the recent block so a reloaded runtime state keeps its summary. The
// existing system block is left intact when msgs carry no genuine system message,
// so reloading a session never wipes the persona/system prompt.
func (m *Manager) SetMessages(msgs []models.AgentMessage) {
	var nonSystem []models.AgentMessage
	for _, msg := range msgs {
		if msg.Role == models.RoleSystem && !isCompactedSummary(msg) {
			m.SetSystemPrompt(msg.Text())
		} else {
			nonSystem = append(nonSystem, msg)
		}
	}
	m.ReplaceRecent(nonSystem)
}

func isCompactedSummary(msg models.AgentMessage) bool {
	if msg.Metadata == nil {
		return false
	}
	v, ok := msg.Metadata["compacted"].(bool)
	return ok && v
}

// Clone returns a deep copy of the manager with independent blocks.
func (m *Manager) Clone() *Manager {
	// Every configured field must be carried over, not just the wired services:
	// the only caller is Agent.WithMode, so anything omitted here is silently
	// reset by a mode switch — a clone that drops cachePolicy would turn caching
	// back to default mid-session, and one that drops thinking would change how
	// the next request is generated.
	other := NewManager(m.budget,
		WithEstimator(m.estimator),
		WithSummarizer(m.summarizer),
		WithWindowPolicy(m.policy),
		WithCacheHintPolicy(m.cachePolicy),
		WithMinRecent(m.keepRecent),
		WithKeepRecentTokens(m.keepRecentTokens),
		WithThinking(m.thinking),
	)
	other.ephemeralReminders = append([]string(nil), m.ephemeralReminders...)
	other.lastUsage, other.hasUsage = m.lastUsage, m.hasUsage
	for _, b := range m.blocks {
		copied := NewBlock(b.Kind, b.Name, b.Stability, b.Priority)
		copied.Messages = append([]models.AgentMessage(nil), b.Messages...)
		copied.Metadata = make(map[string]any)
		for k, v := range b.Metadata {
			copied.Metadata[k] = v
		}
		copied.CacheHint = b.CacheHint
		copied.LastModifiedTurn = b.LastModifiedTurn
		other.SetBlock(copied)
	}
	return other
}

// Stats returns token usage per block and total.
func (m *Manager) Stats() map[string]int {
	stats := make(map[string]int)
	total := 0
	for _, b := range m.Blocks() {
		tokens := m.EstimateTokens(b.Messages)
		stats[string(b.Kind)+":"+b.Name] = tokens
		total += tokens
	}
	stats["total"] = total
	stats["budget_max"] = m.budget.MaxTotal
	stats["budget_target"] = m.budget.TargetTotal
	stats["budget_output_reserve"] = m.budget.ReserveOutput
	stats["drop_limit"] = m.budget.DropLimit()
	// Real provider-reported prompt-token accounting, when a turn has run.
	if rt, ok := m.RealPromptTokens(); ok {
		stats["real_input"] = m.lastUsage.InputTokens
		stats["real_cache_read"] = m.lastUsage.CacheReadTokens
		stats["real_cache_creation"] = m.lastUsage.CacheCreationTokens
		stats["real_prompt_total"] = rt
	}
	// Current multi-level compaction pressure tier (0=none..3=reactive), keyed
	// off real tokens when available, else the heuristic estimate.
	stats["compaction_level"] = int(m.budget.PressureLevel(m.currentTotalTokens()))
	return stats
}

// normalizeBreakpoints sorts breakpoint indices ascending and drops duplicates.
// The prefix anchor and the tail anchor collapse onto the same index whenever the
// conversation holds a single message, and duplicates would each consume one of
// the provider's limited cache_control slots.
func normalizeBreakpoints(bps []int) []int {
	if len(bps) < 2 {
		return bps
	}
	sort.Ints(bps)
	out := bps[:1]
	for _, b := range bps[1:] {
		if b != out[len(out)-1] {
			out = append(out, b)
		}
	}
	return out
}

// DefaultEstimator uses a rough 4-char-per-token heuristic.
func DefaultEstimator(messages []models.AgentMessage) int {
	total := 0
	for _, m := range messages {
		for _, part := range m.Content {
			if t, ok := part.(models.TextContent); ok {
				total += len(t.Text)
			}
		}
	}
	return total / 4
}
