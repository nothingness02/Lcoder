package subagent

import "context"

// This file defines the boundary between the subagent tool (and any other
// consumer) and the in-process host that actually runs subagents. The
// concrete implementation lives in pkg/agenthost; depending on this
// interface keeps pkg/tools/builtin free of the pkg/agent dependency.

// SpawnRequest describes one subagent run.
type SpawnRequest struct {
	Profile Agent
	Task    string
	CWD     string // working directory for the child's file tools; empty = host cwd
	// ParentToolCallID links the run to the parent's subagent tool call so
	// mirrored activity can be rendered nested under it in the TUI.
	ParentToolCallID string
}

// ResumeRequest continues a previously spawned subagent from its journal.
// The profile is restored from the journal; Task is the new instruction.
type ResumeRequest struct {
	AgentID string
	Task    string
	// ParentToolCallID links the run to the parent's resume tool call.
	ParentToolCallID string
}

// Outcome is what the parent gets back from a subagent run.
type Outcome struct {
	AgentID  string
	Summary  string
	Usage    map[string]int
	TimedOut bool
	// Canceled marks a run stopped early by cancellation (user interrupt or
	// turn-budget abort) — distinct from completed so the parent does not
	// treat a partial result as success.
	Canceled bool
	Err      error
}

// Spawner runs subagents to completion, fresh or resumed.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) *Outcome
	Resume(ctx context.Context, req ResumeRequest) *Outcome
}
