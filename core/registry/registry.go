package registry

import (
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// Kind names the sort of definition a reference points at, for error messages
// and export paths.
type Kind string

const (
	KindWorkflow Kind = "workflow"
	KindWorker   Kind = "worker"
)

// workerEntry / workflowEntry pair a registered definition with the canonical
// content hash computed when it was registered — the value immutability checks
// compare against (REQ-VERSION-01) and export/pinning read (REQ-VERSION-02/03).
type workerEntry struct {
	def  domain.Worker
	hash string
}

type workflowEntry struct {
	def  domain.Workflow
	hash string
}

// Registry is the in-memory versioned definition store. It is not safe for
// concurrent registration; register definitions up front (e.g. at CLI startup)
// and then read. Lookup makes it an engine.WorkerSource, so it substitutes for
// the in-memory map an executor was wired with, unchanged.
type Registry struct {
	workflows map[string]workflowEntry
	workers   map[string]workerEntry
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{
		workflows: make(map[string]workflowEntry),
		workers:   make(map[string]workerEntry),
	}
}

// RegisterWorker validates the Worker's semver and stores it at "id@version".
// Re-registering identical content at the same version is a no-op; registering
// *different* content at an already-taken version is a *ConflictError
// (REQ-VERSION-01).
func (r *Registry) RegisterWorker(w domain.Worker) error {
	if w.ID == "" {
		return fmt.Errorf("registry: worker has no id")
	}
	if !ValidVersion(w.Version) {
		return fmt.Errorf("registry: worker %q has an invalid semver version %q", w.ID, w.Version)
	}
	ref := w.ID + "@" + w.Version
	hash, err := canonical.Hash(w)
	if err != nil {
		return fmt.Errorf("registry: hash worker %q: %w", ref, err)
	}
	if existing, ok := r.workers[ref]; ok && existing.hash != hash {
		return &ConflictError{Kind: KindWorker, Ref: ref, Existing: existing.hash, Incoming: hash}
	}
	r.workers[ref] = workerEntry{def: w, hash: hash}
	return nil
}

// RegisterWorkflow validates the Workflow's semver and stores it at
// "id@version", with the same immutability guarantee as RegisterWorker.
func (r *Registry) RegisterWorkflow(wf domain.Workflow) error {
	if wf.ID == "" {
		return fmt.Errorf("registry: workflow has no id")
	}
	if !ValidVersion(wf.Version) {
		return fmt.Errorf("registry: workflow %q has an invalid semver version %q", wf.ID, wf.Version)
	}
	ref := wf.ID + "@" + wf.Version
	hash, err := canonical.Hash(wf)
	if err != nil {
		return fmt.Errorf("registry: hash workflow %q: %w", ref, err)
	}
	if existing, ok := r.workflows[ref]; ok && existing.hash != hash {
		return &ConflictError{Kind: KindWorkflow, Ref: ref, Existing: existing.hash, Incoming: hash}
	}
	r.workflows[ref] = workflowEntry{def: wf, hash: hash}
	return nil
}

// Lookup resolves a worker "id@version" reference, implementing
// engine.WorkerSource so the Registry drops in wherever the in-memory map did.
func (r *Registry) Lookup(ref string) (domain.Worker, bool) {
	e, ok := r.workers[ref]
	return e.def, ok
}

// Workflow resolves a workflow "id@version" reference — the same lookup
// Lookup does for workers, exposed for callers that import a bundle
// (registry.Import) and need the workflow itself back out, not just its
// referenced Workers (M1.14's template gallery: core/server hands both to
// the UI as plain JSON).
func (r *Registry) Workflow(ref string) (domain.Workflow, bool) {
	e, ok := r.workflows[ref]
	return e.def, ok
}

// SoleWorkflow returns the one workflow registered, along with its "id@version"
// ref, when there is exactly one — the common case right after Import(), which
// registers exactly the one workflow Export bundled (plus its Workers). ok is
// false if zero or more than one workflow is registered, so a caller never
// silently picks an arbitrary one out of an ambiguous Registry.
func (r *Registry) SoleWorkflow() (ref string, wf domain.Workflow, ok bool) {
	if len(r.workflows) != 1 {
		return "", domain.Workflow{}, false
	}
	for ref, e := range r.workflows {
		return ref, e.def, true
	}
	return "", domain.Workflow{}, false // unreachable
}

// ContentHash returns the canonical content hash recorded for a registered
// worker or workflow reference, or ok=false if nothing is registered there.
func (r *Registry) ContentHash(ref string) (hash string, ok bool) {
	if e, ok := r.workers[ref]; ok {
		return e.hash, true
	}
	if e, ok := r.workflows[ref]; ok {
		return e.hash, true
	}
	return "", false
}

// DefinitionHashes returns the content hash of every worker wf references that
// is registered here — the map an execution records in its snapshot so replay
// resolves definitions from the pinned record, never the current registry state
// (REQ-VERSION-02). Tool-backed nodes (no worker) and unregistered references
// are omitted rather than erroring: this is provenance capture, not validation.
func (r *Registry) DefinitionHashes(wf domain.Workflow) map[string]string {
	out := make(map[string]string)
	for _, n := range wf.Nodes {
		if n.Worker == "" {
			continue
		}
		if e, ok := r.workers[n.Worker]; ok {
			out[n.Worker] = e.hash
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Workers returns the full resolved Worker definition (goal, contract,
// contextPolicy, tools, model) for every worker wf references that is
// registered here, keyed by "id@version" — the record an execution pins into
// its snapshot so the Inspector (M1.13, REQ-UI-03) can show a node's Contract
// without re-reading the original *.worker.yaml file. Same omission rule as
// DefinitionHashes: tool-backed nodes and unregistered references are skipped.
func (r *Registry) Workers(wf domain.Workflow) map[string]domain.Worker {
	out := make(map[string]domain.Worker)
	for _, n := range wf.Nodes {
		if n.Worker == "" {
			continue
		}
		if e, ok := r.workers[n.Worker]; ok {
			out[n.Worker] = e.def
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
