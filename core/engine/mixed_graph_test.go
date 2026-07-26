package engine_test

import (
	"context"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// TestMixedGraphRunsThroughRealScheduler is the M1.6a acceptance test: a
// graph mixing an LLM-backed node feeding a tool-backed node runs end-to-end
// through the real Scheduler, emitting ToolCalled/ToolResult for the tool
// node and skipping the cache for it — while the worker node still caches
// normally.
func TestMixedGraphRunsThroughRealScheduler(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(0)}
	prov := &fakeProvider{outputs: []string{`{"score":1,"issues":["looks fine"]}`}}
	we := engine.NewWorkerExecutor(workers, fakeRegistry(prov))

	ft := &fakeTool{name: "fake"}
	te := engine.NewToolExecutor(registryWith(ft))

	d := engine.NewDispatchExecutor(we, te)
	s, log := cachingScheduler(t, d)

	wf := &domain.Workflow{
		ID: "mixed", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "review", Worker: "reviewer@1.0.0"},
			{ID: "commit", Tool: &domain.ToolCall{ToolName: "fake", Input: map[string]any{"message": "${review.issues}"}}},
		},
		Edges: []domain.Edge{{From: "review", To: "commit"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	if res.Nodes["review"].State != engine.StateSucceeded || res.Nodes["commit"].State != engine.StateSucceeded {
		t.Fatalf("both nodes should succeed: %+v", res.Nodes)
	}

	toolCalled := countByType(t, log, "e1", domain.ToolCalled)
	toolResult := countByType(t, log, "e1", domain.ToolResult)
	if toolCalled["commit"] != 1 || toolResult["commit"] != 1 {
		t.Errorf("tool node should emit exactly one ToolCalled/ToolResult pair, got called=%v result=%v", toolCalled, toolResult)
	}

	// The tool node must never be a cache hit/miss subject in the audit
	// sense that matters for REQ-WORKER-07 — it should not appear in
	// CacheHit/CacheMiss at all on this first run either; only the worker
	// node participates in the cache lifecycle (CacheMiss on its cold run).
	miss := countByType(t, log, "e1", domain.CacheMiss)
	if _, ok := miss["commit"]; ok {
		t.Errorf("tool-backed node must never emit CacheMiss, got %v", miss)
	}
	if miss["review"] != 1 {
		t.Errorf("worker node should be a cache miss on its cold run, got %v", miss)
	}

	// Re-run unchanged: the worker node is a cache hit; the tool node
	// re-executes (emits a fresh ToolCalled/ToolResult pair) every time.
	callsBefore := prov.callCount()
	res2, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e2", Concurrency: 2})
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if res2.State != domain.ExecutionSucceeded {
		t.Fatalf("run 2 state = %s", res2.State)
	}
	if prov.callCount() != callsBefore {
		t.Errorf("worker node should be a cache hit on re-run, made %d extra model calls", prov.callCount()-callsBefore)
	}
	toolCalled2 := countByType(t, log, "e2", domain.ToolCalled)
	if toolCalled2["commit"] != 1 {
		t.Errorf("tool node should re-execute (never cached) on re-run, got %v", toolCalled2)
	}
}
