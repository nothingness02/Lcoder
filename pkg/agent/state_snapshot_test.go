package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestRuntimeSnapshotRoundTrip(t *testing.T) {
	original := newStateHolder()
	original.SetState(StateExecutingTools)
	original.Steer(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "steer"}))

	ex1 := newExecutor(nil, nil, nil, nil, nil, nil)
	ex1.activateDeferredTool("read")
	ex1.activateDeferredTool("bash")

	rsState := original.snapshot()
	rsExec := ex1.snapshot()

	restored := newStateHolder()
	restored.restore(rsState)

	ex2 := newExecutor(nil, nil, nil, nil, nil, nil)
	ex2.restore(rsExec)

	if restored.State() != StateExecutingTools {
		t.Errorf("restored state = %v, want %v", restored.State(), StateExecutingTools)
	}
	if len(restored.steeringQueue) != 1 || !reflect.DeepEqual(restored.steeringQueue, original.steeringQueue) {
		t.Errorf("steering queue mismatch: got %+v, want %+v", restored.steeringQueue, original.steeringQueue)
	}

	gotDeferred := ex2.activeDeferredNames()
	wantDeferred := []string{"bash", "read"}
	sort.Strings(gotDeferred)
	sort.Strings(wantDeferred)
	if !reflect.DeepEqual(gotDeferred, wantDeferred) {
		t.Errorf("active deferred mismatch: got %v, want %v", gotDeferred, wantDeferred)
	}

	// The original state holder must retain its queues (snapshot/restore do not mutate).
	if len(original.steeringQueue) != 1 {
		t.Errorf("original steering queue was mutated: got length %d", len(original.steeringQueue))
	}

	// Restore should provide a fresh abort channel.
	if original.abortCh == restored.abortCh {
		t.Error("restored abortCh must be a fresh channel")
	}
}
