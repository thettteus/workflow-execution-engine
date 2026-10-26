package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// echoProvider returns a deterministic function of the messages it is given, so
// a node's output changes iff its compiled input changes — exactly what drives
// downstream cache invalidation. It counts calls so a test can prove a cache hit
// skipped the model entirely.
type echoProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *echoProvider) Complete(_ context.Context, msgs []model.Message, _ model.Params) (model.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(string(m.Role))
		sb.WriteString(m.Content)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return model.Response{Content: fmt.Sprintf(`{"v":%q}`, hex.EncodeToString(sum[:8])), InputTokens: 10, OutputTokens: 5}, nil
}

func (p *echoProvider) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func permissiveWorker(id string) domain.Worker {
	return domain.Worker{
		ID: id, Version: "1.0.0", Objective: "produce a value",
		Contract: domain.Contract{Goal: "any object", OutputSchema: map[string]any{"type": "object"}},
		Model:    domain.ModelConfig{Model: "gpt-4o-mini"},
	}
}

// cachingScheduler builds a scheduler over a shared base dir so store + cache
// persist across runs. The same instance is reused for both runs.
func cachingScheduler(t *testing.T, exec engine.NodeExecutor) (*engine.Scheduler, *eventlog.Log) {
	t.Helper()
	base := t.TempDir()
	log := eventlog.New(base)
	return engine.New(exec, store.New(base), log, cache.New(base)), log
}

// countByType tallies events of type ty per node id for one execution.
func countByType(t *testing.T, log *eventlog.Log, execID string, ty domain.EventType) map[string]int {
	t.Helper()
	events, err := log.ReadAll(execID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	out := map[string]int{}
	for _, ev := range events {
		if ev.Type == ty {
			out[ev.NodeID]++
		}
	}
	return out
}

// TestSecondRunIsAllCacheHitsAtZeroCost is M1.6 acceptance #1: re-running an
// unchanged workflow is 100% cache hits at $0.00, with zero model calls.
func TestSecondRunIsAllCacheHitsAtZeroCost(t *testing.T) {
	workers := engine.MapWorkerSource{
		"A@1.0.0": permissiveWorker("A"),
		"B@1.0.0": permissiveWorker("B"),
		"C@1.0.0": permissiveWorker("C"),
	}
	prov := &echoProvider{}
	reg := model.NewRegistry()
	reg.Register("openai", prov)
	ex := engine.NewWorkerExecutor(workers, reg)
	s, log := cachingScheduler(t, ex)

	wf := &domain.Workflow{
		ID: "chain", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "A", Worker: "A@1.0.0"}, {ID: "B", Worker: "B@1.0.0"}, {ID: "C", Worker: "C@1.0.0"}},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "B", To: "C"}},
	}

	// Run 1: cold — three model calls, cost > 0, three CacheMiss.
	r1, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "run1", Concurrency: 2})
	if err != nil || r1.State != domain.ExecutionSucceeded {
		t.Fatalf("run1: state=%s err=%v", r1.State, err)
	}
	if prov.count() != 3 {
		t.Fatalf("run1 should make 3 model calls, made %d", prov.count())
	}
	if r1.SpentCostUSD <= 0 {
		t.Fatalf("run1 should cost > 0, got %v", r1.SpentCostUSD)
	}
	if miss := countByType(t, log, "run1", domain.CacheMiss); len(miss) != 3 {
		t.Errorf("run1 should be 3 misses, got %v", miss)
	}

	// Run 2: warm, unchanged — no new model calls, $0, three CacheHit.
	callsBefore := prov.count()
	r2, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "run2", Concurrency: 2})
	if err != nil || r2.State != domain.ExecutionSucceeded {
		t.Fatalf("run2: state=%s err=%v", r2.State, err)
	}
	if prov.count() != callsBefore {
		t.Errorf("run2 should make zero model calls, made %d extra", prov.count()-callsBefore)
	}
	if r2.SpentCostUSD != 0 {
		t.Errorf("run2 should cost $0.00, got %v", r2.SpentCostUSD)
	}
	hits := countByType(t, log, "run2", domain.CacheHit)
	if hits["A"] != 1 || hits["B"] != 1 || hits["C"] != 1 {
		t.Errorf("run2 should be 100%% cache hits, got %v", hits)
	}
}

// TestChangingOneNodeReExecutesOnlyItsCone is M1.6 acceptance #2: bump one node's
// contract and only that node plus its downstream cone re-executes; upstream and
// siblings stay cached (REQ-CACHE-03).
func TestChangingOneNodeReExecutesOnlyItsCone(t *testing.T) {
	// Graph: R -> A, R -> B (A,B siblings), A -> C (C downstream of A).
	workers := engine.MapWorkerSource{
		"R@1.0.0": permissiveWorker("R"),
		"A@1.0.0": permissiveWorker("A"),
		"B@1.0.0": permissiveWorker("B"),
		"C@1.0.0": permissiveWorker("C"),
	}
	prov := &echoProvider{}
	reg := model.NewRegistry()
	reg.Register("openai", prov)
	ex := engine.NewWorkerExecutor(workers, reg)
	s, log := cachingScheduler(t, ex)

	wf := &domain.Workflow{
		ID: "cone", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "R", Worker: "R@1.0.0"}, {ID: "A", Worker: "A@1.0.0"},
			{ID: "B", Worker: "B@1.0.0"}, {ID: "C", Worker: "C@1.0.0"},
		},
		Edges: []domain.Edge{{From: "R", To: "A"}, {From: "R", To: "B"}, {From: "A", To: "C"}},
	}

	if _, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "cold", Concurrency: 2}); err != nil {
		t.Fatalf("cold run: %v", err)
	}

	// Change A's contract only — its key (and its output) changes.
	a := workers["A@1.0.0"]
	a.Contract.Goal = "produce a DIFFERENT value"
	workers["A@1.0.0"] = a

	if _, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "warm", Concurrency: 2}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	hits := countByType(t, log, "warm", domain.CacheHit)
	miss := countByType(t, log, "warm", domain.CacheMiss)

	// R (upstream) and B (sibling) are untouched → hits.
	if hits["R"] != 1 || hits["B"] != 1 {
		t.Errorf("R and B should be cache hits, got hits=%v", hits)
	}
	// A (changed) and C (downstream of A, changed input) → misses.
	if miss["A"] != 1 || miss["C"] != 1 {
		t.Errorf("A and its downstream C should re-execute, got miss=%v", miss)
	}
	// And the converse must not happen.
	if hits["A"] != 0 || hits["C"] != 0 {
		t.Errorf("A/C must not be hits: %v", hits)
	}
	if miss["R"] != 0 || miss["B"] != 0 {
		t.Errorf("R/B must not re-execute: %v", miss)
	}
}

// TestChangingWorkflowInputInvalidatesWorkerCache is the regression for the
// PR-review path: changing a run input such as prUrl must never reuse a model
// artifact recorded for a different target, even if the node has no upstream
// artifacts of its own.
func TestChangingWorkflowInputInvalidatesWorkerCache(t *testing.T) {
	workers := engine.MapWorkerSource{
		"reviewer@1.0.0": permissiveWorker("reviewer"),
	}
	prov := &echoProvider{}
	reg := model.NewRegistry()
	reg.Register("openai", prov)
	ex := engine.NewWorkerExecutor(workers, reg)
	s, log := cachingScheduler(t, ex)

	wf := &domain.Workflow{
		ID:      "input-cache",
		Version: "1.0.0",
		Inputs:  []domain.InputDecl{{Name: "prUrl", Required: true}},
		Nodes:   []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}},
		Edges:   []domain.Edge{},
	}

	if _, err := s.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "first",
		Inputs:      map[string]string{"prUrl": "https://api.github.com/repos/a/a/pulls/1"},
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := s.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "second",
		Inputs:      map[string]string{"prUrl": "https://api.github.com/repos/b/b/pulls/2"},
	}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	hits := countByType(t, log, "second", domain.CacheHit)
	miss := countByType(t, log, "second", domain.CacheMiss)
	if hits["review"] != 0 {
		t.Fatalf("changed prUrl reused reviewer cache: hits=%v", hits)
	}
	if miss["review"] != 1 {
		t.Fatalf("changed prUrl should be a reviewer cache miss, got misses=%v", miss)
	}
	if prov.count() != 2 {
		t.Fatalf("reviewer should have made two model calls for two prUrl values, got %d", prov.count())
	}
}

func TestCacheHitWithMissingArtifactEmitsDiagnosticMiss(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	workers := engine.MapWorkerSource{"A@1.0.0": permissiveWorker("A")}
	prov := &echoProvider{}
	reg := model.NewRegistry()
	reg.Register("openai", prov)
	s := engine.New(engine.NewWorkerExecutor(workers, reg), store.New(base), log, cache.New(base))
	wf := &domain.Workflow{ID: "cache-repair", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "A@1.0.0"}}}

	first, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "first"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	hash := first.Nodes["A"].Hash
	if err := os.Remove(filepath.Join(base, "artifacts", hash)); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}

	if _, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "second"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	events, err := log.ReadAll("second")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != domain.CacheMiss || ev.NodeID != "A" {
			continue
		}
		diag, ok := ev.Payload["diagnostic"].(map[string]any)
		if !ok {
			continue
		}
		if diag["kind"] == "cache" && diag["code"] == "cache_artifact_missing" && diag["likelyFix"] != "" {
			return
		}
	}
	t.Fatalf("second run did not explain missing cache artifact: %#v", events)
}

// TestInPlaceWorkerEditInvalidatesCache: version immutability is enforced per
// registry, i.e. per process, so a *.worker.yaml whose objective was edited
// without a version bump reaches the next run as a different definition at the
// same "id@version". The cache key covers the definition itself, so that run
// is a miss — never the old objective's output served from cache.
func TestInPlaceWorkerEditInvalidatesCache(t *testing.T) {
	base := t.TempDir()
	prov := &echoProvider{}
	reg := model.NewRegistry()
	reg.Register("openai", prov)
	wf := &domain.Workflow{ID: "edit", Version: "1.0.0", Nodes: []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}}}

	run := func(execID, objective string) {
		t.Helper()
		w := permissiveWorker("reviewer")
		w.Objective = objective
		ex := engine.NewWorkerExecutor(engine.MapWorkerSource{"reviewer@1.0.0": w}, reg)
		s := engine.New(ex, store.New(base), eventlog.New(base), cache.New(base))
		if _, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: execID}); err != nil {
			t.Fatalf("%s: %v", execID, err)
		}
	}
	run("before", "review for style")
	run("after", "review for security")

	if hits := countByType(t, eventlog.New(base), "after", domain.CacheHit); hits["review"] != 0 {
		t.Fatalf("edited objective at the same version was served from cache: hits=%v", hits)
	}
	if prov.count() != 2 {
		t.Fatalf("model calls = %d, want 2 (the edit must re-execute)", prov.count())
	}
}
