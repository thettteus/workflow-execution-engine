package registry_test

import (
	"errors"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/registry"
)

// The Registry must satisfy engine.WorkerSource so it drops in wherever the
// in-memory map stood, without the executor changing (worker_executor.go:17-19).
var _ engine.WorkerSource = (*registry.Registry)(nil)

func worker(id, version, objective string) domain.Worker {
	return domain.Worker{
		ID:        id,
		Version:   version,
		Objective: objective,
		Contract:  domain.Contract{Goal: "g", OutputSchema: map[string]any{"type": "object"}},
		Model:     domain.ModelConfig{Provider: "openai", Model: "gpt-4o"},
	}
}

// TestRegisterAndLookup covers the happy path: a registered worker resolves by
// its "id@version" reference, and an unregistered one does not.
func TestRegisterAndLookup(t *testing.T) {
	r := registry.New()
	if err := r.RegisterWorker(worker("reviewer", "1.0.0", "review code")); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	got, ok := r.Lookup("reviewer@1.0.0")
	if !ok {
		t.Fatal("Lookup(reviewer@1.0.0) = not found, want found")
	}
	if got.Objective != "review code" {
		t.Errorf("resolved worker objective = %q, want %q", got.Objective, "review code")
	}
	if _, ok := r.Lookup("reviewer@9.9.9"); ok {
		t.Error("Lookup(reviewer@9.9.9) = found, want not found")
	}
}

// TestWorkersResolvesEveryNodeReference covers registry.Workers(wf) (REQ-UI-03):
// it must return the full resolved Worker for every node reference the
// workflow makes and registry knows about, key by "id@version", and quietly
// omit tool-backed nodes and unregistered references rather than erroring —
// mirroring DefinitionHashes' provenance-capture, not-validation contract.
func TestWorkersResolvesEveryNodeReference(t *testing.T) {
	r := registry.New()
	if err := r.RegisterWorker(worker("reviewer", "1.0.0", "review code")); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	wf := domain.Workflow{
		Nodes: []domain.Node{
			{ID: "review", Worker: "reviewer@1.0.0"},
			{ID: "test", Tool: &domain.ToolCall{ToolName: "terminal"}},
			{ID: "ghost", Worker: "unregistered@1.0.0"},
		},
	}

	got := r.Workers(wf)
	if len(got) != 1 {
		t.Fatalf("Workers() = %+v, want exactly one resolved entry", got)
	}
	w, ok := got["reviewer@1.0.0"]
	if !ok || w.Objective != "review code" {
		t.Fatalf("Workers()[reviewer@1.0.0] = %+v, ok=%v, want the registered reviewer", w, ok)
	}
}

// TestWorkersNilWhenNothingResolves: a workflow with no worker-backed node (or
// none registered) yields nil, not an empty map — matching DefinitionHashes so
// the snapshot stays byte-identical to a run that pinned nothing.
func TestWorkersNilWhenNothingResolves(t *testing.T) {
	r := registry.New()
	wf := domain.Workflow{Nodes: []domain.Node{{ID: "test", Tool: &domain.ToolCall{ToolName: "terminal"}}}}
	if got := r.Workers(wf); got != nil {
		t.Errorf("Workers() = %+v, want nil", got)
	}
}

// TestWorkflowResolvesRegisteredRef mirrors TestRegisterAndLookup for
// Workflow — the accessor M1.14's template-gallery server handler uses to get
// the workflow back out of an imported bundle's Registry.
func TestWorkflowResolvesRegisteredRef(t *testing.T) {
	r := registry.New()
	wf := domain.Workflow{ID: "wf", Version: "1.0.0", Budget: domain.Budget{}}
	if err := r.RegisterWorkflow(wf); err != nil {
		t.Fatalf("RegisterWorkflow: %v", err)
	}
	got, ok := r.Workflow("wf@1.0.0")
	if !ok || got.ID != "wf" {
		t.Fatalf("Workflow(wf@1.0.0) = %+v, ok=%v, want the registered workflow", got, ok)
	}
	if _, ok := r.Workflow("wf@9.9.9"); ok {
		t.Error("Workflow(wf@9.9.9) = found, want not found")
	}
}

// TestSoleWorkflowRoundTripsThroughImport is the template-gallery use case
// (M1.14): Export -> Import -> SoleWorkflow recovers the one bundled
// workflow without the caller needing to already know its ref.
func TestSoleWorkflowRoundTripsThroughImport(t *testing.T) {
	r := registry.New()
	if err := r.RegisterWorker(worker("reviewer", "1.0.0", "review code")); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	wf := domain.Workflow{ID: "wf", Version: "1.0.0", Nodes: []domain.Node{{ID: "a", Worker: "reviewer@1.0.0"}}}
	if err := r.RegisterWorkflow(wf); err != nil {
		t.Fatalf("RegisterWorkflow: %v", err)
	}

	bundle, err := r.Export("wf", "1.0.0")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	imported, err := registry.Import(bundle)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	ref, got, ok := imported.SoleWorkflow()
	if !ok {
		t.Fatal("SoleWorkflow() = not ok, want the one imported workflow")
	}
	if ref != "wf@1.0.0" || got.ID != "wf" {
		t.Errorf("SoleWorkflow() = (%q, %+v), want (\"wf@1.0.0\", id=wf)", ref, got)
	}
}

func TestSoleWorkflowFalseWhenNotExactlyOne(t *testing.T) {
	r := registry.New()
	if _, _, ok := r.SoleWorkflow(); ok {
		t.Error("SoleWorkflow() on an empty registry = ok, want false")
	}
}

// TestReRegisterIdenticalContentIsNoOp: registering byte-identical content at
// the same version again is allowed (idempotent), not a conflict.
func TestReRegisterIdenticalContentIsNoOp(t *testing.T) {
	r := registry.New()
	w := worker("reviewer", "1.0.0", "review code")
	if err := r.RegisterWorker(w); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.RegisterWorker(w); err != nil {
		t.Errorf("re-registering identical content should be a no-op, got: %v", err)
	}
}

// TestImmutableVersionRejectsMutation is the REQ-VERSION-01 acceptance path:
// changing a released version's content without bumping the version is a
// *ConflictError naming the reference.
func TestImmutableVersionRejectsMutation(t *testing.T) {
	r := registry.New()
	if err := r.RegisterWorker(worker("reviewer", "1.0.0", "review code")); err != nil {
		t.Fatalf("first register: %v", err)
	}

	err := r.RegisterWorker(worker("reviewer", "1.0.0", "review code differently"))
	if err == nil {
		t.Fatal("re-registering different content at 1.0.0 should fail")
	}
	var ce *registry.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConflictError, got %T: %v", err, err)
	}
	if ce.Ref != "reviewer@1.0.0" || ce.Kind != registry.KindWorker {
		t.Errorf("conflict = %+v, want worker reviewer@1.0.0", ce)
	}
	if ce.Existing == ce.Incoming || ce.Existing == "" || ce.Incoming == "" {
		t.Errorf("conflict should name two distinct non-empty hashes, got existing=%q incoming=%q", ce.Existing, ce.Incoming)
	}

	// Bumping the version is the sanctioned path — the same new content at a new
	// version registers cleanly, and both versions coexist.
	if err := r.RegisterWorker(worker("reviewer", "2.0.0", "review code differently")); err != nil {
		t.Errorf("registering the new content at a bumped version should succeed, got: %v", err)
	}
	if _, ok := r.Lookup("reviewer@1.0.0"); !ok {
		t.Error("original version should still be resolvable after a bump")
	}
	if _, ok := r.Lookup("reviewer@2.0.0"); !ok {
		t.Error("bumped version should be resolvable")
	}
}

// TestRegisterRejectsInvalidSemver: a non-semver version is refused at
// registration, before it can pollute the store.
func TestRegisterRejectsInvalidSemver(t *testing.T) {
	r := registry.New()
	if err := r.RegisterWorker(worker("reviewer", "latest", "review")); err == nil {
		t.Error("registering a worker at version \"latest\" should fail")
	}
	if err := r.RegisterWorkflow(domain.Workflow{ID: "wf", Version: "1.0"}); err == nil {
		t.Error("registering a workflow at version \"1.0\" should fail")
	}
}
