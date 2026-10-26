package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// lastPayload returns the payload of the last event of typ in events.
func lastPayload(t *testing.T, events []domain.Event, typ domain.EventType) map[string]any {
	t.Helper()
	var p map[string]any
	for _, ev := range events {
		if ev.Type == typ {
			p = ev.Payload
		}
	}
	if p == nil {
		t.Fatalf("no %s event recorded", typ)
	}
	return p
}

// TestRejectedAttemptSpendIsCounted is REQ-BUDGET-03 across a contract retry: a
// model call whose output the contract rejected was still billed, so its tokens
// count toward the node's total and the execution's spend — not just the final
// successful attempt's.
func TestRejectedAttemptSpendIsCounted(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(1)}
	p := &fakeProvider{outputs: []string{`{"score":"bad"}`, `{"score":0.5,"issues":[]}`}} // 15 tokens per call
	s, log := newScheduler(t, engine.NewWorkerExecutor(workers, fakeRegistry(p)))

	wf := &domain.Workflow{ID: "sp", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SpentTokens != 30 || res.Nodes["A"].Tokens != 30 {
		t.Errorf("spent tokens = %d (node %d), want 30 for two billed calls", res.SpentTokens, res.Nodes["A"].Tokens)
	}
	events, _ := log.ReadAll("e1")
	if got := lastPayload(t, events, domain.WorkerFinished)["tokens"]; got != float64(30) {
		t.Errorf("WorkerFinished tokens = %v, want 30", got)
	}
	if got := lastPayload(t, events, domain.Retry)["attemptTokens"]; got != float64(15) {
		t.Errorf("Retry attemptTokens = %v, want 15", got)
	}
}

// TestFailedNodeSpendIsCounted: a node that exhausts its contract retries still
// spent every attempt; the Failure event and the run's totals record it.
func TestFailedNodeSpendIsCounted(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(1)}
	p := &fakeProvider{outputs: []string{`{"nope":1}`}} // always malformed
	s, log := newScheduler(t, engine.NewWorkerExecutor(workers, fakeRegistry(p)))

	wf := &domain.Workflow{ID: "sp", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err == nil {
		t.Fatal("a terminal contract violation must fail the run")
	}
	if res.SpentTokens != 30 || res.Nodes["A"].Tokens != 30 {
		t.Errorf("spent tokens = %d (node %d), want 30 for two rejected calls", res.SpentTokens, res.Nodes["A"].Tokens)
	}
	events, _ := log.ReadAll("e1")
	if got := lastPayload(t, events, domain.Failure)["tokens"]; got != float64(30) {
		t.Errorf("Failure tokens = %v, want 30", got)
	}
}

// TestFailedSpendCanExhaustBudget: rejected attempts count toward the budget, so
// a node that keeps producing invalid output halts the run with BudgetExceeded
// even though it never succeeded.
func TestFailedSpendCanExhaustBudget(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(1)}
	p := &fakeProvider{outputs: []string{`{"nope":1}`}}
	s, log := newScheduler(t, engine.NewWorkerExecutor(workers, fakeRegistry(p)))

	wf := &domain.Workflow{ID: "sp", Version: "1.0.0", Nodes: []domain.Node{
		{ID: "A", Worker: "reviewer@1.0.0", OnFailure: &domain.FailurePolicy{Mode: domain.FailContinue}},
	}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "e1", Concurrency: 1,
		Budget: domain.Budget{MaxTokens: 20},
	})
	if !errors.Is(err, engine.ErrBudgetExceeded) || !res.BudgetExceeded {
		t.Fatalf("err = %v, BudgetExceeded = %v; want the rejected spend to exhaust the budget", err, res.BudgetExceeded)
	}
	if n := eventCount(t, log, "e1", domain.BudgetExceeded); n != 1 {
		t.Errorf("BudgetExceeded events = %d, want 1", n)
	}
}

// TestResumeContinuesRecordedBudget: a resumed run (every approval is one) is the
// same execution, so spend its earlier segments recorded still counts. A run
// that already exhausted its budget must not dispatch anything on resume.
func TestResumeContinuesRecordedBudget(t *testing.T) {
	stub := newStub()
	stub.cost["A"] = 0.01
	stub.cost["B"] = 0.01
	s, log := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "line", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B")},
		Edges: []domain.Edge{{From: "A", To: "B"}},
	}
	opts := engine.RunOptions{ExecutionID: "e1", Concurrency: 1, Budget: domain.Budget{MaxCostUSD: 0.01}}
	if _, err := s.Run(context.Background(), wf, opts); !errors.Is(err, engine.ErrBudgetExceeded) {
		t.Fatalf("first run err = %v, want ErrBudgetExceeded", err)
	}

	res, err := s.Resume(context.Background(), "e1")
	if !errors.Is(err, engine.ErrBudgetExceeded) {
		t.Fatalf("resume err = %v, want ErrBudgetExceeded (the recorded spend already crosses the limit)", err)
	}
	if stub.startCount("B") != 0 {
		t.Error("resume dispatched B although the execution's budget was already exhausted")
	}
	if res.SpentCostUSD != 0.01 {
		t.Errorf("resumed SpentCostUSD = %v, want the execution's recorded 0.01", res.SpentCostUSD)
	}
	if n := eventCount(t, log, "e1", domain.BudgetExceeded); n != 2 {
		t.Errorf("BudgetExceeded events = %d, want one per halted segment (2)", n)
	}
}
