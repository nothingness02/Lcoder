package task

import (
	"testing"
)

func TestManagerReplaceAll(t *testing.T) {
	tm := NewManager()

	reconciled, warnings, err := tm.ReplaceAll([]Task{
		{Text: "a", Status: StatusPending},
		{Text: "b", Status: StatusInProgress},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if len(reconciled) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(reconciled))
	}
}

func TestManagerReplaceAllReconcilesMissingUnfinished(t *testing.T) {
	tm := NewManager()
	_, _, _ = tm.ReplaceAll([]Task{
		{Text: "old1", Status: StatusPending},
		{Text: "old2", Status: StatusInProgress},
		{Text: "old3", Status: StatusDone},
	})

	reconciled, warnings, err := tm.ReplaceAll([]Task{
		{Text: "new", Status: StatusPending},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings for old1 and old2, got %v", warnings)
	}
	if len(reconciled) != 3 {
		t.Fatalf("expected 3 tasks (new + old1 + old2), got %d", len(reconciled))
	}

	seen := make(map[string]bool)
	for _, t := range reconciled {
		seen[t.Text] = true
	}
	if !seen["old1"] || !seen["old2"] || !seen["new"] {
		t.Fatalf("missing expected tasks: %+v", reconciled)
	}
	if seen["old3"] {
		t.Fatalf("done task old3 should have been dropped")
	}
}

func TestManagerReplaceAllStatusProgression(t *testing.T) {
	tm := NewManager()
	_, _, _ = tm.ReplaceAll([]Task{
		{Text: "x", Status: StatusPending},
	})

	reconciled, _, err := tm.ReplaceAll([]Task{
		{Text: "x", Status: StatusDone},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0].Status != StatusDone {
		t.Fatalf("expected x done, got %+v", reconciled)
	}
}

func TestManagerReplaceAllInvalid(t *testing.T) {
	tm := NewManager()
	_, _, err := tm.ReplaceAll([]Task{
		{Text: "", Status: StatusPending},
	})
	if err == nil {
		t.Fatal("expected error for empty text")
	}

	_, _, err = tm.ReplaceAll([]Task{
		{Text: "x", Status: "bogus"},
	})
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestManagerFormatReminder(t *testing.T) {
	tm := NewManager()
	if r := tm.FormatReminder(); r != "" {
		t.Fatalf("expected empty reminder for no tasks, got %q", r)
	}

	_, _, _ = tm.ReplaceAll([]Task{
		{Text: "a", Status: StatusDone},
		{Text: "b", Status: StatusPending},
	})
	r := tm.FormatReminder()
	if r == "" {
		t.Fatal("expected non-empty reminder for unfinished task")
	}

	_, _, _ = tm.ReplaceAll([]Task{
		{Text: "a", Status: StatusDone},
		{Text: "b", Status: StatusDone},
	})
	if r := tm.FormatReminder(); r != "" {
		t.Fatalf("expected empty reminder when all done, got %q", r)
	}
}

func TestManagerSubscribe(t *testing.T) {
	tm := NewManager()
	var called []Task
	tm.Subscribe(func(ts []Task) {
		called = make([]Task, len(ts))
		copy(called, ts)
	})

	_, _, _ = tm.ReplaceAll([]Task{{Text: "x", Status: StatusPending}})
	if len(called) != 1 || called[0].Text != "x" {
		t.Fatalf("subscriber did not receive snapshot: %+v", called)
	}
}

func TestManagerSnapshotRestore(t *testing.T) {
	tm := NewManager()
	_, _, _ = tm.ReplaceAll([]Task{
		{Text: "a", Status: StatusDone},
		{Text: "b", Status: StatusInProgress},
	})

	snap := tm.Snapshot()
	tm2 := NewManager()
	if err := tm2.Restore(snap); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if got := tm2.List(); len(got) != 2 {
		t.Fatalf("expected 2 tasks after restore, got %d", len(got))
	}
}
