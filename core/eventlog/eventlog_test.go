package eventlog_test

import (
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
)

// TestReconstructTimelineFromDiskAlone is the M1.2 acceptance test: an
// execution directory alone, with no in-process state, is enough to rebuild the
// ordered timeline. The write and read phases use *separate* Log instances so
// nothing can leak between them.
func TestReconstructTimelineFromDiskAlone(t *testing.T) {
	base := t.TempDir()
	const execID = "exec-1"
	start := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	budget := domain.Budget{MaxCostUSD: 1, MaxTokens: 1000, MaxDurationMs: 1000, MaxRetriesPerNode: 1}
	snapshot := domain.Execution{
		ID: execID, WorkflowRef: "pr-review@1.0.0", State: domain.ExecutionRunning,
		Graph: domain.Workflow{
			ID: "pr-review", Version: "1.0.0",
			Nodes:  []domain.Node{{ID: "a", Worker: "w@1.0.0"}},
			Budget: budget,
		},
		Budget:    domain.BudgetStatus{Limit: budget},
		StartedAt: start,
	}

	// --- Write phase. ---
	writer := eventlog.New(base)
	if err := writer.WriteSnapshot(execID, snapshot); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	want := []domain.EventType{
		domain.ExecutionStarted,
		domain.WorkerStarted,
		domain.ArtifactCreated,
		domain.WorkerFinished,
		domain.ExecutionFinished,
	}
	for i, et := range want {
		ev := domain.Event{
			Type:        et,
			Timestamp:   start.Add(time.Duration(i) * time.Second),
			ExecutionID: execID,
			NodeID:      "a",
		}
		if err := writer.Append(execID, ev); err != nil {
			t.Fatalf("Append %s: %v", et, err)
		}
	}

	// --- Read phase: a fresh Log, no shared in-process state. ---
	reader := eventlog.New(base)

	events, err := reader.ReadAll(execID)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d", len(want), len(events))
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Errorf("event %d out of order: want %s, got %s", i, want[i], events[i].Type)
		}
		if i > 0 && !events[i].Timestamp.After(events[i-1].Timestamp) {
			t.Errorf("timeline not strictly ordered at index %d", i)
		}
	}

	var got domain.Execution
	if err := reader.ReadSnapshot(execID, &got); err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if got.ID != execID || got.WorkflowRef != snapshot.WorkflowRef {
		t.Errorf("snapshot identity lost: %+v", got)
	}
	if len(got.Graph.Nodes) != 1 || got.Graph.Nodes[0].ID != "a" {
		t.Errorf("snapshot graph not reconstructed: %+v", got.Graph)
	}
	if !got.StartedAt.Equal(start) {
		t.Errorf("snapshot timestamp lost: got %v, want %v", got.StartedAt, start)
	}
}

func TestReadAllMissingIsError(t *testing.T) {
	r := eventlog.New(t.TempDir())
	if _, err := r.ReadAll("does-not-exist"); err == nil {
		t.Error("expected an error reading a non-existent log")
	}
}
