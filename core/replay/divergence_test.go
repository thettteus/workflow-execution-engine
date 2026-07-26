package replay_test

import (
	"context"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

func divergenceFor(t *testing.T, divs []replay.NodeDivergence, id string) replay.NodeDivergence {
	t.Helper()
	for _, d := range divs {
		if d.NodeID == id {
			return d
		}
	}
	t.Fatalf("no divergence entry for node %q", id)
	return replay.NodeDivergence{}
}

// TestReexecuteAndDivergenceLabelChangedNodeAndDownstream is the M1.7
// acceptance test combining REQ-REPLAY-02 and REQ-REPLAY-03: re-executing an
// execution whose middle node's contract changed (in the registry, since the
// original ran — see cachingExecutor's doc comment) re-executes that node and
// everything downstream of it, while the unrelated upstream node is served
// from cache — and Divergence reports exactly that partition.
func TestReexecuteAndDivergenceLabelChangedNodeAndDownstream(t *testing.T) {
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

	if _, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "orig"}); err != nil {
		t.Fatalf("original Run: %v", err)
	}

	aud := replay.NewAuditor(eventlog.New(base), store.New(base))
	origTL, err := aud.Audit("orig")
	if err != nil {
		t.Fatalf("Audit(orig): %v", err)
	}

	// The registry now resolves "wb@1" to a new contract — the frozen graph
	// itself is untouched; only what that reference means today has changed.
	exec.mu.Lock()
	exec.contractVersion["wb@1"] = "v2"
	exec.mu.Unlock()

	reexec := replay.NewReexecuter(eventlog.New(base), sched)
	if _, err := reexec.Reexecute(context.Background(), "orig", "reexec"); err != nil {
		t.Fatalf("Reexecute: %v", err)
	}
	reexecTL, err := aud.Audit("reexec")
	if err != nil {
		t.Fatalf("Audit(reexec): %v", err)
	}

	if got := exec.callCount("a"); got != 1 {
		t.Errorf("a called %d times, want 1 (cache hit on reexecution)", got)
	}
	if got := exec.callCount("b"); got != 2 {
		t.Errorf("b called %d times, want 2 (its contract changed)", got)
	}
	if got := exec.callCount("c"); got != 2 {
		t.Errorf("c called %d times, want 2 (downstream of the changed node)", got)
	}

	divs := replay.Divergence(origTL, reexecTL)
	if got := divergenceFor(t, divs, "a").Status; got != replay.Cached {
		t.Errorf("a status = %s, want cached", got)
	}
	if got := divergenceFor(t, divs, "b").Status; got != replay.ReExecuted {
		t.Errorf("b status = %s, want re-executed", got)
	}
	if got := divergenceFor(t, divs, "c").Status; got != replay.ReExecuted {
		t.Errorf("c status = %s, want re-executed (downstream cascade)", got)
	}
}

// TestDivergenceClassifiesAddedAndRemoved covers the two Timelines-diverged-
// in-shape cases: a node that only exists in one side.
func TestDivergenceClassifiesAddedAndRemoved(t *testing.T) {
	original := replay.Timeline{
		Nodes: map[string]replay.NodeRecord{
			"kept":    {State: engine.StateSucceeded, Hash: "h1"},
			"removed": {State: engine.StateSucceeded, Hash: "h2"},
		},
	}
	reexecuted := replay.Timeline{
		Nodes: map[string]replay.NodeRecord{
			"kept":  {State: engine.StateSucceeded, Hash: "h1"},
			"added": {State: engine.StateSucceeded, Hash: "h3"},
		},
	}

	divs := replay.Divergence(original, reexecuted)
	if got := divergenceFor(t, divs, "kept").Status; got != replay.Cached {
		t.Errorf("kept status = %s, want cached", got)
	}
	if got := divergenceFor(t, divs, "removed").Status; got != replay.Removed {
		t.Errorf("removed status = %s, want removed", got)
	}
	if got := divergenceFor(t, divs, "added").Status; got != replay.Added {
		t.Errorf("added status = %s, want added", got)
	}
}
