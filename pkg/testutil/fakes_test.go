package testutil

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

var errBusy = errors.New("fake: agent is running")

func TestSetModeErrorLeavesModeUntouched(t *testing.T) {
	ag := &FakeAgent{ModeName: "code", SetModeErr: errors.New("no such mode")}
	if err := ag.SetMode("plan"); err == nil {
		t.Fatal("expected SetModeErr")
	}
	if got := ag.Mode(); got != "code" {
		t.Fatalf("failed SetMode must not change the mode, got %q", got)
	}
}

func TestSetModeSuccessFlipsMode(t *testing.T) {
	ag := &FakeAgent{}
	if err := ag.SetMode("plan"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := ag.Mode(); got != "plan" {
		t.Fatalf("Mode = %q, want plan", got)
	}
}

// BusyErr refuses run submissions and state-changing operations without side
// effects, mirroring the host's in-flight refusal.
func TestBusyErrRefusesStateChangesWithoutSideEffects(t *testing.T) {
	ag := &FakeAgent{
		Messages:     []models.AgentMessage{models.UserMessage("hi")},
		SessionIDVal: "s1",
		SessionMsgs:  map[string][]models.AgentMessage{"s2": {models.UserMessage("q2")}},
		BusyErr:      errBusy,
	}

	if err := ag.Prompt(context.Background(), models.UserMessage("more")); !errors.Is(err, errBusy) {
		t.Fatalf("Prompt = %v, want BusyErr", err)
	}
	if len(ag.Prompts) != 0 {
		t.Fatalf("busy Prompt must not record, got %v", ag.Prompts)
	}
	if err := ag.Continue(context.Background()); !errors.Is(err, errBusy) {
		t.Fatalf("Continue = %v, want BusyErr", err)
	}
	if err := ag.SetMode("plan"); !errors.Is(err, errBusy) {
		t.Fatalf("SetMode = %v, want BusyErr", err)
	}
	if ag.ModeName != "" {
		t.Fatalf("busy SetMode must not flip ModeName, got %q", ag.ModeName)
	}
	if err := ag.OpenSession("s2"); !errors.Is(err, errBusy) {
		t.Fatalf("OpenSession = %v, want BusyErr", err)
	}
	if ag.SessionIDVal != "s1" {
		t.Fatalf("busy OpenSession must not switch sessions, got %q", ag.SessionIDVal)
	}
	if err := ag.NewSession(); !errors.Is(err, errBusy) {
		t.Fatalf("NewSession = %v, want BusyErr", err)
	}
	if ag.NewSessionCount != 0 {
		t.Fatalf("busy NewSession must not count, got %d", ag.NewSessionCount)
	}
	if err := ag.TruncateAfter(""); !errors.Is(err, errBusy) {
		t.Fatalf("TruncateAfter = %v, want BusyErr", err)
	}
	if len(ag.TruncateAfterCalls) != 0 {
		t.Fatalf("busy TruncateAfter must not record, got %v", ag.TruncateAfterCalls)
	}
	if len(ag.Messages) != 1 {
		t.Fatalf("busy refusals must not touch Messages, got %v", ag.Messages)
	}
	if err := ag.RestoreCheckpoint("s1"); !errors.Is(err, errBusy) {
		t.Fatalf("RestoreCheckpoint = %v, want BusyErr", err)
	}
	if ag.RestoredCheckpoint != "" {
		t.Fatalf("busy RestoreCheckpoint must not record, got %q", ag.RestoredCheckpoint)
	}
}

func TestBusyErrClearedRestoresOperation(t *testing.T) {
	ag := &FakeAgent{BusyErr: errBusy}
	if err := ag.SetMode("plan"); !errors.Is(err, errBusy) {
		t.Fatalf("SetMode = %v, want BusyErr", err)
	}
	ag.BusyErr = nil
	if err := ag.SetMode("plan"); err != nil {
		t.Fatalf("SetMode after clearing BusyErr: %v", err)
	}
	if got := ag.Mode(); got != "plan" {
		t.Fatalf("Mode = %q, want plan", got)
	}
}
