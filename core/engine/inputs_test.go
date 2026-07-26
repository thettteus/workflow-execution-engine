package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

func workflowWithInputs(decls ...domain.InputDecl) *domain.Workflow {
	return &domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes:  []domain.Node{node("a")},
		Edges:  []domain.Edge{},
		Inputs: decls,
		Budget: domain.Budget{MaxCostUSD: 1},
	}
}

// TestRunFailsFastOnMissingRequiredInput is REQ-INPUT-01's enforcement half: a
// required input with no supplied value and no default halts the run before
// any node dispatches (PRIN-05, "before the call, not after").
func TestRunFailsFastOnMissingRequiredInput(t *testing.T) {
	exec := newStub()
	sched, _ := newScheduler(t, exec)
	wf := workflowWithInputs(domain.InputDecl{Name: "prUrl", Required: true})

	_, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1"})
	if !errors.Is(err, engine.ErrMissingInput) {
		t.Fatalf("Run err = %v, want ErrMissingInput", err)
	}
	if exec.startCount("a") != 0 {
		t.Errorf("node dispatched despite a missing required input: startCount = %d", exec.startCount("a"))
	}
}

// TestRunUsesDefaultWhenInputNotSupplied confirms a Default satisfies a
// Required declaration with no supplied value.
func TestRunUsesDefaultWhenInputNotSupplied(t *testing.T) {
	exec := newStub()
	sched, _ := newScheduler(t, exec)
	wf := workflowWithInputs(domain.InputDecl{Name: "prUrl", Required: true, Default: "https://example.com/default"})

	_, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := exec.wfInputsSeen()["prUrl"]; got != "https://example.com/default" {
		t.Errorf("prUrl seen by executor = %q, want the default", got)
	}
}

// TestRunSuppliedInputOverridesDefault confirms a caller-supplied value wins
// over a declared Default.
func TestRunSuppliedInputOverridesDefault(t *testing.T) {
	exec := newStub()
	sched, _ := newScheduler(t, exec)
	wf := workflowWithInputs(domain.InputDecl{Name: "prUrl", Default: "https://example.com/default"})

	_, err := sched.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "e1",
		Inputs:      map[string]string{"prUrl": "https://example.com/supplied"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := exec.wfInputsSeen()["prUrl"]; got != "https://example.com/supplied" {
		t.Errorf("prUrl seen by executor = %q, want the supplied value", got)
	}
}

// TestRunWithNoDeclaredInputsIsUnaffected confirms a Workflow with no Inputs
// runs exactly as before this feature existed — no new required field, no
// behavior change for the hundreds of existing workflows that never opt in.
func TestRunWithNoDeclaredInputsIsUnaffected(t *testing.T) {
	exec := newStub()
	sched, _ := newScheduler(t, exec)
	wf := &domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes:  []domain.Node{node("a")},
		Edges:  []domain.Edge{},
		Budget: domain.Budget{MaxCostUSD: 1},
	}
	if _, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exec.startCount("a") != 1 {
		t.Errorf("startCount = %d, want 1", exec.startCount("a"))
	}
}

// TestResumeRestoresInputs confirms a resumed run does not need --input
// re-supplied: the resolved values from the original run are persisted in the
// Snapshot and restored by Resume, so a required input with no default still
// resolves correctly for whatever nodes remain (as opposed to failing
// ErrMissingInput on a second, value-less "run").
func TestResumeRestoresInputs(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)

	wf := &domain.Workflow{
		ID: "line", Version: "1.0.0",
		Nodes:  []domain.Node{node("A"), node("B")},
		Edges:  []domain.Edge{{From: "A", To: "B"}},
		Inputs: []domain.InputDecl{{Name: "prUrl", Required: true}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	stub1 := newStub()
	stub1.blockUntilCancel["B"] = true
	stub1.onStart = func(id string) {
		if id == "B" {
			cancel()
		}
	}
	res1, _ := engine.New(stub1, st, log, cache.New(base)).Run(ctx, wf, engine.RunOptions{
		ExecutionID: "e1",
		Concurrency: 1,
		Inputs:      map[string]string{"prUrl": "https://example.com/42"},
	})
	if res1.State != domain.ExecutionCancelled {
		t.Fatalf("run 1 state = %s, want cancelled", res1.State)
	}

	stub2 := newStub()
	res2, err := engine.New(stub2, st, log, cache.New(base)).Resume(context.Background(), "e1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res2.State != domain.ExecutionSucceeded {
		t.Fatalf("resume state = %s, want succeeded", res2.State)
	}
	if got := stub2.wfInputsSeen()["prUrl"]; got != "https://example.com/42" {
		t.Errorf("resumed node saw prUrl = %q, want the original run's supplied value", got)
	}
}
