// Package checkpoint defines a portable snapshot DTO and the interfaces used to
// save and restore agent runtime state.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// CurrentVersion is the only checkpoint format version accepted by UnmarshalJSON.
const CurrentVersion = 1

// Checkpoint reasons.
const (
	ReasonManual = "manual"
	ReasonAuto   = "auto"
	ReasonCrash  = "crash"
)

// Checkpoint is a portable, serializable snapshot of an agent session.
type Checkpoint struct {
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	Session   *SessionSnapshot  `json:"session"`
	Agent     *AgentSnapshot    `json:"agent"`
	Context   *ContextSnapshot  `json:"context"`
	Runtime   *RuntimeSnapshot  `json:"runtime"`
}

// SessionSnapshot identifies the session and checkpoint lineage.
type SessionSnapshot struct {
	SessionID        string `json:"session_id"`
	CheckpointID     string `json:"checkpoint_id"`
	ParentCheckpoint string `json:"parent_checkpoint,omitempty"`
	Reason           string `json:"reason,omitempty"`
	ConfigHash       string `json:"config_hash,omitempty"`
}

// AgentSnapshot captures the runtime configuration that affects agent behavior
// and cannot be derived from the context manager alone.
type AgentSnapshot struct {
	Mode              string             `json:"mode"`
	Model             models.ModelRef    `json:"model"`
	MaxTurns          int                `json:"max_turns,omitempty"`
	ToolExecutionMode models.ExecutionMode `json:"tool_execution_mode,omitempty"`
	DeferredTools     bool               `json:"deferred_tools,omitempty"`
	CoreTools         []string           `json:"core_tools,omitempty"`
}

// MarshalJSON sets default Version and CreatedAt before serialization.
func (cp Checkpoint) MarshalJSON() ([]byte, error) {
	if cp.Version == 0 || cp.Version != CurrentVersion {
		cp.Version = CurrentVersion
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	type alias Checkpoint
	return json.Marshal((*alias)(&cp))
}

// UnmarshalJSON validates the checkpoint version before finishing deserialization.
func (cp *Checkpoint) UnmarshalJSON(data []byte) error {
	type alias Checkpoint
	aux := (*alias)(cp)
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.Version != CurrentVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrVersionMismatch, aux.Version, CurrentVersion)
	}
	return nil
}

// ContextSnapshot captures the state of a contextmgr.Manager.
type ContextSnapshot struct {
	Budget             contextmgr.TokenBudget  `json:"budget"`
	Blocks             []BlockSnapshot         `json:"blocks"`
	EphemeralReminders []string                `json:"ephemeral_reminders,omitempty"`
	LastUsage          *contextmgr.RealUsage   `json:"last_usage,omitempty"`
	CachePolicy        string                  `json:"cache_policy,omitempty"`
	MinRecent          int                     `json:"min_recent,omitempty"`
}

// BlockSnapshot captures block-level runtime metadata without messages.
// Messages are owned by the session store and loaded separately on startup.
type BlockSnapshot struct {
	Kind             string                `json:"kind"`
	Name             string                `json:"name"`
	Priority         int                   `json:"priority"`
	Stability        string                `json:"stability"`
	Metadata         map[string]any        `json:"metadata,omitempty"`
	CacheHint        string                `json:"cache_hint,omitempty"`
	LastModifiedTurn int                   `json:"last_modified_turn,omitempty"`
}

// RuntimeSnapshot captures the agent runtime state.
type RuntimeSnapshot struct {
	State            int               `json:"state"`
	Turn             int               `json:"turn,omitempty"`
	IsAtTurnBoundary bool              `json:"is_at_turn_boundary,omitempty"`
	SteeringQueue    []models.AgentMessage `json:"steering_queue,omitempty"`
	FollowUpQueue    []models.AgentMessage `json:"follow_up_queue,omitempty"`
	ActiveDeferred   map[string]bool       `json:"active_deferred,omitempty"`
	TaskManagerState *task.ManagerState    `json:"task_manager_state,omitempty"`
}

// Source produces a Checkpoint representing the current state.
type Source interface {
	Checkpoint() (*Checkpoint, error)
}

// Target restores state from a Checkpoint.
type Target interface {
	Restore(*Checkpoint) error
}

// Store persists and retrieves checkpoints by identifier.
type Store interface {
	Save(id string, cp *Checkpoint) error
	Load(id string) (*Checkpoint, error)
	// List returns the identifiers of all stored checkpoints.
	List() ([]string, error)
	Delete(id string) error
}
