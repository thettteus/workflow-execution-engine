package replay_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// cachingExecutor is a NodeExecutor + CacheKeyer test double whose cache key
// is derived from an externally mutable "contract version" per Worker
// reference (contractVersion), independent of the frozen graph itself — it
// stands in for a Worker/Contract registry that can be edited between an
// original run and a later re-execution, which is what REQ-REPLAY-02's
// "only invalidated nodes reach a model" is actually about: the graph the
// re-execution runs is byte-identical (the frozen snapshot), but what a
// Worker reference resolves to today may not be.
type cachingExecutor struct {
	mu              sync.Mutex
	calls           map[string]int
	contractVersion map[string]string // node.Worker -> current version stand-in
}

func newCachingExecutor() *cachingExecutor {
	return &cachingExecutor{calls: map[string]int{}, contractVersion: map[string]string{}}
}

func (e *cachingExecutor) Execute(ctx context.Context, req engine.NodeRequest) (engine.NodeResult, error) {
	e.mu.Lock()
	e.calls[req.Node.ID]++
	ver := e.contractVersion[req.Node.Worker]
	e.mu.Unlock()
	// A real Worker's output depends on what it was given, so this double's
	// does too: otherwise a downstream node's re-executed output could
	// coincidentally byte-match its prior run even though its upstream input
	// changed, which no real executor would produce.
	var inputHashes string
	for _, in := range req.Inputs {
		inputHashes += in.Hash
	}
	content := []byte(fmt.Sprintf(`{"node":%q,"worker":%q,"contract":%q,"inputs":%q}`, req.Node.ID, req.Node.Worker, ver, inputHashes))
	return engine.NodeResult{Content: content, Type: domain.ArtifactJSON, CostUSD: 0.05, Tokens: 11}, nil
}

func (e *cachingExecutor) CacheKey(node domain.Node, inputs []engine.NodeInput, workflowInputs map[string]string) (string, bool) {
	e.mu.Lock()
	ver := e.contractVersion[node.Worker]
	e.mu.Unlock()
	hashes := make([]string, 0, len(inputs))
	for _, in := range inputs {
		hashes = append(hashes, in.Hash)
	}
	return cache.Key(cache.Inputs{WorkerID: node.Worker, ContractHash: ver, InputArtifactHashes: hashes, WorkflowInputs: workflowInputs}), true
}

func (e *cachingExecutor) callCount(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[id]
}

func chainWorkflow() *domain.Workflow {
	return &domain.Workflow{
		ID: "chain", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "a", Worker: "wa@1"},
			{ID: "b", Worker: "wb@1"},
			{ID: "c", Worker: "wc@1"},
		},
		Edges: []domain.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	}
}

// TestReexecuteReusesCacheForUnchangedNodes is the REQ-REPLAY-02 acceptance
// path: re-executing a snapshot whose registry-resolved contracts haven't
// changed since the original run costs nothing — every node is a cache hit,
// and the executor is never called again.
func TestReexecuteReusesCacheForUnchangedNodes(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)
	c := cache.New(base)

	exec := newCachingExecutor()
	for _, w := range []string{"wa@1", "wb@1", "wc@1"} {
		exec.contractVersion[w] = "v1"
	}
	sched := engine.New(exec, st, log, c)
	wf := chainWorkflow()

	origRes, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "orig"})
	if err != nil {
		t.Fatalf("original Run: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if got := exec.callCount(id); got != 1 {
			t.Fatalf("node %q called %d times on the original run, want 1", id, got)
		}
	}

	reexec := replay.NewReexecuter(eventlog.New(base), sched)
	newRes, err := reexec.Reexecute(context.Background(), "orig", "reexec")
	if err != nil {
		t.Fatalf("Reexecute: %v", err)
	}

	for _, id := range []string{"a", "b", "c"} {
		if got := exec.callCount(id); got != 1 {
			t.Errorf("node %q called %d times after reexecution, want still 1 (cache hit)", id, got)
		}
		if newRes.Nodes[id].Hash != origRes.Nodes[id].Hash {
			t.Errorf("node %q hash changed across reexecution: %q vs %q", id, origRes.Nodes[id].Hash, newRes.Nodes[id].Hash)
		}
	}
	if newRes.SpentCostUSD != 0 || newRes.SpentTokens != 0 {
		t.Errorf("reexecution spent %v USD / %v tokens, want 0/0 (all cache hits)", newRes.SpentCostUSD, newRes.SpentTokens)
	}
}

// TestReexecuteUnknownOriginalErrors mirrors Audit's own missing-execution
// error path.
func TestReexecuteUnknownOriginalErrors(t *testing.T) {
	base := t.TempDir()
	exec := newCachingExecutor()
	sched := engine.New(exec, store.New(base), eventlog.New(base), cache.New(base))
	reexec := replay.NewReexecuter(eventlog.New(base), sched)
	if _, err := reexec.Reexecute(context.Background(), "does-not-exist", "new"); err == nil {
		t.Error("expected an error reexecuting a non-existent original execution")
	}
}
