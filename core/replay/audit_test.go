package replay_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// recordingExecutor counts every Execute call so a test can prove Audit never
// triggers one — the property REQ-REPLAY-01 requires ("zero model calls").
type recordingExecutor struct {
	mu    sync.Mutex
	calls int
	fail  map[string]bool
}

func (e *recordingExecutor) Execute(ctx context.Context, req engine.NodeRequest) (engine.NodeResult, error) {
	e.mu.Lock()
	e.calls++
	shouldFail := e.fail[req.Node.ID]
	e.mu.Unlock()
	if shouldFail {
		return engine.NodeResult{}, engine.Fatal(errors.New("boom"))
	}
	content := []byte(`{"node":"` + req.Node.ID + `"}`)
	return engine.NodeResult{Content: content, Type: domain.ArtifactJSON, CostUSD: 0.02, Tokens: 7}, nil
}

func (e *recordingExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestAuditReconstructsSucceededFailedAndSkippedNodes is the M1.7 acceptance
// path for REQ-REPLAY-01: a real run producing one succeeded, one failed
// (under a continue policy so the run still finishes), and one conditionally
// skipped node is audited back from disk alone, matching what the live Result
// reported — and the executor is never called again to do it.
func TestAuditReconstructsSucceededFailedAndSkippedNodes(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)

	exec := &recordingExecutor{fail: map[string]bool{"failing": true}}
	sched := engine.New(exec, st, log, cache.New(base))

	wf := &domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "ok", Worker: "w@1.0.0"},
			{ID: "failing", Worker: "w@1.0.0", OnFailure: &domain.FailurePolicy{Mode: domain.FailContinue}},
			{ID: "gated", Worker: "w@1.0.0"},
		},
		Edges: []domain.Edge{
			{From: "ok", To: "gated", Condition: &domain.Condition{Path: "node", Op: domain.OpEq, Value: "nope"}},
		},
	}

	const execID = "exec-audit-1"
	res, err := sched.Run(context.Background(), wf, engine.RunOptions{ExecutionID: execID})
	if err != nil {
		t.Fatalf("Run: %v (continue policy should not surface an error)", err)
	}
	if res.State != domain.ExecutionFailed {
		t.Fatalf("execution state = %s, want failed (the continue-policy node failed)", res.State)
	}
	callsAfterRun := exec.callCount()

	aud := replay.NewAuditor(eventlog.New(base), store.New(base))
	tl, err := aud.Audit(execID)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	if exec.callCount() != callsAfterRun {
		t.Errorf("Audit invoked the executor: calls before=%d after=%d", callsAfterRun, exec.callCount())
	}
	if tl.ExecutionID != execID {
		t.Errorf("ExecutionID = %q, want %q", tl.ExecutionID, execID)
	}
	if tl.Workflow.ID != "wf" || len(tl.Workflow.Nodes) != 3 {
		t.Errorf("workflow not reconstructed: %+v", tl.Workflow)
	}
	if len(tl.Events) == 0 {
		t.Fatal("expected a non-empty event timeline")
	}

	okRec := tl.Nodes["ok"]
	wantOK := res.Nodes["ok"]
	if okRec.State != engine.StateSucceeded || okRec.Hash != wantOK.Hash || string(okRec.Content) != `{"node":"ok"}` {
		t.Errorf("ok node record = %+v, want hash %q content matching live run", okRec, wantOK.Hash)
	}
	if okRec.CostUSD != 0.02 || okRec.Tokens != 7 {
		t.Errorf("ok node cost/tokens = %v/%v, want 0.02/7", okRec.CostUSD, okRec.Tokens)
	}

	failRec := tl.Nodes["failing"]
	if failRec.State != engine.StateFailed || failRec.Err == "" {
		t.Errorf("failing node record = %+v, want StateFailed with a recorded error", failRec)
	}

	gatedRec := tl.Nodes["gated"]
	if gatedRec.State != engine.StateSkipped {
		t.Errorf("gated node state = %v, want StateSkipped (its condition never held)", gatedRec.State)
	}

	if tl.SpentCostUSD != res.SpentCostUSD || tl.SpentTokens != res.SpentTokens {
		t.Errorf("Timeline totals = %v/%v, want %v/%v", tl.SpentCostUSD, tl.SpentTokens, res.SpentCostUSD, res.SpentTokens)
	}
}

// TestAuditExposesResolvedInputs is REQ-INPUT-01's audit half: the resolved
// value behind a declared workflow input is readable from the audit record
// alone, the same way DefinitionHashes/Workers already are (M1.13).
func TestAuditExposesResolvedInputs(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)

	exec := &recordingExecutor{}
	sched := engine.New(exec, st, log, cache.New(base))

	wf := &domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes:  []domain.Node{{ID: "a", Worker: "w@1.0.0"}},
		Edges:  []domain.Edge{},
		Inputs: []domain.InputDecl{{Name: "prUrl", Required: true}},
	}

	const execID = "exec-audit-inputs"
	if _, err := sched.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: execID,
		Inputs:      map[string]string{"prUrl": "https://example.com/42"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	aud := replay.NewAuditor(eventlog.New(base), store.New(base))
	tl, err := aud.Audit(execID)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got := tl.Inputs["prUrl"]; got != "https://example.com/42" {
		t.Errorf("tl.Inputs[prUrl] = %q, want the resolved value", got)
	}
}

// TestAuditExposesFrozenConnectionRefs is REQ-UI-19's audit substrate: model
// identity can be paired with the exact non-secret connection resolution from
// snapshot.json instead of today's mutable Settings.
func TestAuditExposesFrozenConnectionRefs(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)
	sched := engine.New(&recordingExecutor{}, st, log, cache.New(base))
	wf := &domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}},
		Edges: []domain.Edge{},
	}

	const execID = "exec-audit-connections"
	if _, err := sched.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: execID,
		ConnectionRefs: map[string]engine.ConnectionRef{
			"kimi-prod": {
				ID: "kimi-prod", Label: "Kimi production", Kind: "model-provider",
				Type: "openai-compatible", BaseURL: "https://api.moonshot.ai/v1",
			},
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	tl, err := replay.NewAuditor(eventlog.New(base), store.New(base)).Audit(execID)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	got := tl.ConnectionRefs["kimi-prod"]
	if got.ID != "kimi-prod" || got.BaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("ConnectionRefs = %+v", tl.ConnectionRefs)
	}
}

// TestAuditUnknownExecutionErrors mirrors eventlog's own
// TestReadAllMissingIsError: auditing an execution id that was never run is
// an error, not a zero-value Timeline.
func TestAuditUnknownExecutionErrors(t *testing.T) {
	base := t.TempDir()
	aud := replay.NewAuditor(eventlog.New(base), store.New(base))
	if _, err := aud.Audit("does-not-exist"); err == nil {
		t.Error("expected an error auditing a non-existent execution")
	}
}
