package registry_test

import (
	"context"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// stubExec is a trivial NodeExecutor: it does no real work, so this test can
// focus purely on definition-hash pinning, which is orthogonal to what a node
// executes (the registry computes the pins; the engine only records them).
type stubExec struct{}

func (stubExec) Execute(ctx context.Context, req engine.NodeRequest) (engine.NodeResult, error) {
	return engine.NodeResult{Content: []byte(`{"n":"` + req.Node.ID + `"}`), Type: domain.ArtifactJSON}, nil
}

// TestSnapshotPinsDefinitionHashesForReplay is the REQ-VERSION-02 acceptance
// path: an execution records the content hashes of the definitions it used, and
// auditing that execution later reads those pinned hashes from the snapshot —
// not the current registry, which has since moved on. Audit holds no registry
// reference at all, so "reads the record, not latest" is structural.
func TestSnapshotPinsDefinitionHashesForReplay(t *testing.T) {
	base := t.TempDir()

	reg := registry.New()
	if err := reg.RegisterWorker(worker("rev", "1.0.0", "review v1")); err != nil {
		t.Fatalf("register v1: %v", err)
	}

	wf := domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "a", Worker: "rev@1.0.0"}},
	}
	pins := reg.DefinitionHashes(wf)
	if pins["rev@1.0.0"] == "" {
		t.Fatalf("DefinitionHashes did not pin rev@1.0.0: %v", pins)
	}
	workers := reg.Workers(wf)
	if workers["rev@1.0.0"].Objective != "review v1" {
		t.Fatalf("Workers did not pin rev@1.0.0's v1 definition: %+v", workers["rev@1.0.0"])
	}

	sched := engine.New(stubExec{}, store.New(base), eventlog.New(base), cache.New(base))
	opts := engine.RunOptions{ExecutionID: "old", DefinitionHashes: pins, Workers: workers}
	if _, err := sched.Run(context.Background(), &wf, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The registry moves on: new, different content ships at a bumped version.
	// (Mutating rev@1.0.0 itself would be refused — REQ-VERSION-01; the point
	// here is that the *snapshot* is what replay reads, whatever the registry
	// holds now.)
	if err := reg.RegisterWorker(worker("rev", "2.0.0", "review v2 — rewritten")); err != nil {
		t.Fatalf("register v2: %v", err)
	}

	tl, err := replay.NewAuditor(eventlog.New(base), store.New(base)).Audit("old")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	wantV1, _ := reg.ContentHash("rev@1.0.0")
	if tl.DefinitionHashes["rev@1.0.0"] != wantV1 {
		t.Errorf("audited pin for rev@1.0.0 = %q, want %q (the content hash frozen at run time)", tl.DefinitionHashes["rev@1.0.0"], wantV1)
	}
	if v2, _ := reg.ContentHash("rev@2.0.0"); tl.DefinitionHashes["rev@1.0.0"] == v2 {
		t.Error("audited pin equals the bumped version's hash — replay read current registry state, not the pinned record")
	}
	if got := tl.Workers["rev@1.0.0"].Objective; got != "review v1" {
		t.Errorf("audited Worker for rev@1.0.0 has objective %q, want the v1 definition frozen at run time (registry now holds v2)", got)
	}
}
