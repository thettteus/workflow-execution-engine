package eventlog_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
)

// writeChain writes a snapshot and a short run of events, returning the base dir.
func writeChain(t *testing.T, execID string) string {
	t.Helper()
	base := t.TempDir()
	log := eventlog.New(base)

	snap := domain.Execution{
		ID: execID, WorkflowRef: "pr-review@1.0.0", State: domain.ExecutionRunning,
		Graph: domain.Workflow{ID: "pr-review", Version: "1.0.0",
			Nodes: []domain.Node{{ID: "a", Worker: "w@1.0.0"}}},
	}
	if err := log.WriteSnapshot(execID, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	start := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	types := []domain.EventType{
		domain.ExecutionStarted, domain.WorkerStarted,
		domain.ArtifactCreated, domain.WorkerFinished, domain.ExecutionFinished,
	}
	for i, ty := range types {
		ev := domain.Event{Type: ty, Timestamp: start.Add(time.Duration(i) * time.Second), ExecutionID: execID, NodeID: "a"}
		if err := log.Append(execID, ev); err != nil {
			t.Fatalf("Append %s: %v", ty, err)
		}
	}
	return base
}

// TestVerifyCleanChain is the happy path (REQ-EVENT-03): an untouched log
// verifies clean, and every event chains from the snapshot forward.
func TestVerifyCleanChain(t *testing.T) {
	const execID = "e1"
	base := writeChain(t, execID)

	// A fresh Log (no in-memory chain head) must still verify from disk alone.
	if err := eventlog.New(base).Verify(execID); err != nil {
		t.Fatalf("clean chain should verify, got: %v", err)
	}
}

// TestVerifyDetectsTamper is the M1.4 acceptance test (REQ-EVENT-03): corrupt
// one line of a finished execution's events.jsonl and Verify fails, naming the
// break point.
func TestVerifyDetectsTamper(t *testing.T) {
	const execID = "e1"
	base := writeChain(t, execID)
	path := filepath.Join(base, "executions", execID, "events.jsonl")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 event lines, got %d", len(lines))
	}
	// Tamper with the 3rd event's payload without touching its recorded prevHash.
	// The edit changes that event's hash, so the 4th event's prevHash no longer
	// matches — the break surfaces at line 4.
	lines[2] = strings.Replace(lines[2], `"type":"ArtifactCreated"`, `"type":"ArtifactCreated","tampered":true`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write tampered log: %v", err)
	}

	err = eventlog.New(base).Verify(execID)
	if err == nil {
		t.Fatal("Verify should fail on a tampered log")
	}
	var ce *eventlog.ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ChainError, got %T: %v", err, err)
	}
	if ce.Line != 4 {
		t.Errorf("break named at line %d, want 4 (the successor of the edited line)", ce.Line)
	}
}

// TestVerifyDetectsGenesisBreak confirms a genesis event whose prevHash no
// longer matches the snapshot hash is caught at line 1 — a swapped-out snapshot
// or a rewritten first line cannot pass.
func TestVerifyDetectsGenesisBreak(t *testing.T) {
	const execID = "e1"
	base := writeChain(t, execID)
	path := filepath.Join(base, "executions", execID, "events.jsonl")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	var first domain.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first event: %v", err)
	}
	first.PrevHash = strings.Repeat("0", 64) // bogus genesis link
	edited, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	lines[0] = string(edited)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write tampered log: %v", err)
	}

	var ce *eventlog.ChainError
	if err := eventlog.New(base).Verify(execID); !errors.As(err, &ce) {
		t.Fatalf("want *ChainError, got %v", err)
	} else if ce.Line != 1 {
		t.Errorf("genesis break named at line %d, want 1", ce.Line)
	}
}
