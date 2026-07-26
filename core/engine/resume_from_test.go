package engine_test

import (
	"context"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// TestResumeFromReexecutesNodeAndDownstream is REQ-CTRL-03's retry-from-node: a
// fully-succeeded chain A→B→C→D, then ResumeFrom(C) re-runs C and D while A and
// B are reused from the record (never re-executed) — the "keep the rest" half of
// REQ-CTRL-04.
func TestResumeFromReexecutesNodeAndDownstream(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)
	wf := &domain.Workflow{
		ID: "line", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B"), node("C"), node("D")},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "B", To: "C"}, {From: "C", To: "D"}},
	}

	// Run 1: full success, so all four nodes are recorded finished.
	stub1 := newStub()
	res1, err := engine.New(stub1, st, log, cache.New(base)).Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.State != domain.ExecutionSucceeded {
		t.Fatalf("run 1 state = %s, want succeeded", res1.State)
	}

	// ResumeFrom C: A,B reused; C,D re-run.
	stub2 := newStub()
	res2, err := engine.New(stub2, st, log, cache.New(base)).ResumeFrom(context.Background(), "e1", "C")
	if err != nil {
		t.Fatalf("ResumeFrom: %v", err)
	}
	if res2.State != domain.ExecutionSucceeded {
		t.Fatalf("resume-from state = %s, want succeeded", res2.State)
	}
	if stub2.startCount("A") != 0 || stub2.startCount("B") != 0 {
		t.Errorf("upstream nodes must not re-run: A=%d B=%d", stub2.startCount("A"), stub2.startCount("B"))
	}
	if stub2.startCount("C") == 0 || stub2.startCount("D") == 0 {
		t.Errorf("from-node and downstream must re-run: C=%d D=%d", stub2.startCount("C"), stub2.startCount("D"))
	}
}

// TestResumeFromUnknownNodeErrors: naming a node not in the workflow is a clear
// error, not a silent no-op.
func TestResumeFromUnknownNodeErrors(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)
	wf := &domain.Workflow{ID: "line", Version: "1.0.0", Nodes: []domain.Node{node("A")}}
	if _, err := engine.New(newStub(), st, log, cache.New(base)).Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := engine.New(newStub(), st, log, cache.New(base)).ResumeFrom(context.Background(), "e1", "ZZZ"); err == nil {
		t.Fatal("ResumeFrom with unknown node should error")
	}
}
