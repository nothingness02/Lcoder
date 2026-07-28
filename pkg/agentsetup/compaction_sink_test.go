package agentsetup

import (
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

type fakeRecorder struct {
	entries   []string
	firstKept []string
	mirrored  int
	entryErr  error
}

func (f *fakeRecorder) AppendCompactionEntry(summary, firstKeptEntryID string, _ int) error {
	if f.entryErr != nil {
		return f.entryErr
	}
	f.entries = append(f.entries, summary)
	f.firstKept = append(f.firstKept, firstKeptEntryID)
	return nil
}

func (f *fakeRecorder) AppendMissing(msgs []models.AgentMessage) error {
	f.mirrored += len(msgs)
	return nil
}

func TestSessionCompactionSinkRecordsFold(t *testing.T) {
	rec := &fakeRecorder{}
	sink := SessionCompactionSink(func() CompactionRecorder { return rec })

	err := sink(contextmgr.FoldResult{
		Committed: true, Summary: "SUMMARY", FirstKeptID: "kept-1", TokensBefore: 900,
	}, []models.AgentMessage{models.UserMessage("kept")})
	if err != nil {
		t.Fatalf("sink: %v", err)
	}
	if len(rec.entries) != 1 || rec.entries[0] != "SUMMARY" {
		t.Fatalf("entries = %v", rec.entries)
	}
	if rec.firstKept[0] != "kept-1" {
		t.Errorf("firstKeptEntryID = %q, want kept-1", rec.firstKept[0])
	}
	if rec.mirrored != 1 {
		t.Errorf("kept tail must be mirrored to disk, mirrored=%d", rec.mirrored)
	}
}

// The recorder is resolved per fold, so a session swapped mid-run receives the
// fold — a sink that captured the startup session would write to the wrong file.
func TestSessionCompactionSinkFollowsActiveSession(t *testing.T) {
	first, second := &fakeRecorder{}, &fakeRecorder{}
	active := NewActiveSession(first)
	sink := SessionCompactionSink(active.Get)

	res := contextmgr.FoldResult{Committed: true, Summary: "S1", FirstKeptID: "a"}
	if err := sink(res, nil); err != nil {
		t.Fatal(err)
	}
	active.Set(second)
	res.Summary = "S2"
	if err := sink(res, nil); err != nil {
		t.Fatal(err)
	}

	if len(first.entries) != 1 || first.entries[0] != "S1" {
		t.Errorf("first session entries = %v, want [S1]", first.entries)
	}
	if len(second.entries) != 1 || second.entries[0] != "S2" {
		t.Errorf("second session entries = %v, want [S2]", second.entries)
	}
}

func TestSessionCompactionSinkPropagatesError(t *testing.T) {
	rec := &fakeRecorder{entryErr: errors.New("disk full")}
	sink := SessionCompactionSink(func() CompactionRecorder { return rec })
	if err := sink(contextmgr.FoldResult{Summary: "S"}, nil); err == nil {
		t.Fatal("a recorder failure must reach the caller")
	}
}

func TestSessionCompactionSinkNilCases(t *testing.T) {
	if SessionCompactionSink(nil) != nil {
		t.Error("a nil resolver must yield no sink")
	}
	sink := SessionCompactionSink(func() CompactionRecorder { return nil })
	if err := sink(contextmgr.FoldResult{Summary: "S"}, nil); err != nil {
		t.Errorf("no active session must be a no-op, got %v", err)
	}
	// An empty summary means nothing was committed worth recording.
	rec := &fakeRecorder{}
	s2 := SessionCompactionSink(func() CompactionRecorder { return rec })
	if err := s2(contextmgr.FoldResult{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.entries) != 0 {
		t.Errorf("empty summary must not be recorded, got %v", rec.entries)
	}
}
