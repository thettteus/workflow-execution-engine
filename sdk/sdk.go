// Package sdk is the Go authoring SDK: build a workflow in code and get exactly
// the same canonical definition a YAML file would produce (REQ-SDK-01). It is
// the same module as the engine and embeds it directly — no subprocess, no
// serialization boundary at authoring time. The SDK is a third door into the
// same room: a workflow authored here content-hashes identically to the
// hand-written YAML (PRIN-03/04).
//
// The builder tracks a frontier — the set of nodes a subsequent step depends
// on. Worker/Tool add one node after the frontier (the common linear case);
// Parallel fans out into several; Merge fans a multi-node frontier back into
// one. Example (the flagship shape — 3 reviewers → fixer → test → commit):
//
//	wf, err := sdk.New("pr-review", "1.0.0").
//		Budget(budget).
//		Parallel(
//			sdk.Worker("review-security", secReviewer),
//			sdk.Worker("review-style", styleReviewer),
//			sdk.Worker("review-correctness", correctReviewer),
//		).
//		Merge("fix", fixer).
//		Tool("test", testRun).
//		Tool("commit", commit).
//		Build()
package sdk

import (
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// Spec is one node in a Parallel fan-out, created by the package-level Worker or
// Tool constructors.
type Spec struct {
	nodeID string
	node   domain.Node
	worker *domain.Worker // set for worker-backed specs; nil for tool-backed
}

// Worker returns a worker-backed node spec for use inside Parallel. The graph
// node id and the Worker are separate: the node references the Worker by its
// "id@version".
func Worker(nodeID string, w domain.Worker) Spec {
	return Spec{nodeID: nodeID, node: domain.Node{ID: nodeID, Worker: w.ID + "@" + w.Version}, worker: &w}
}

// DescribeWorker returns a copy of w with its optional human-facing
// description set. The description remains part of the canonical Worker
// definition while the Contract compiler deliberately excludes it from model
// input (REQ-WORKER-08). Keeping this as a value helper lets it compose with
// Worker, Builder.Worker, Parallel, and Merge without a second Worker model.
func DescribeWorker(w domain.Worker, description string) domain.Worker {
	w.Description = description
	return w
}

// Tool returns a tool-backed node spec for use inside Parallel.
func Tool(nodeID string, tc domain.ToolCall) Spec {
	call := tc
	return Spec{nodeID: nodeID, node: domain.Node{ID: nodeID, Tool: &call}}
}

// Builder accumulates nodes, edges, and the Workers they reference into a
// domain.Workflow. It is not safe for concurrent use; build on one goroutine.
type Builder struct {
	id, version string
	budget      domain.Budget
	nodes       []domain.Node
	edges       []domain.Edge
	workers     map[string]domain.Worker // "id@version" → definition, for Run
	frontier    []string                 // node ids a next step depends on
	seen        map[string]bool          // duplicate-node-id guard
	err         error                    // first error, surfaced at Build
}

// New starts a workflow builder at id@version.
func New(id, version string) *Builder {
	return &Builder{
		id:      id,
		version: version,
		workers: make(map[string]domain.Worker),
		seen:    make(map[string]bool),
	}
}

// Budget sets the workflow budget (required by the schema).
func (b *Builder) Budget(budget domain.Budget) *Builder {
	b.budget = budget
	return b
}

// add appends a node depending on the given parents, records any referenced
// Worker, and advances the frontier to just this node.
func (b *Builder) add(s Spec, parents []string) {
	if b.err != nil {
		return
	}
	if b.seen[s.nodeID] {
		b.err = fmt.Errorf("sdk: duplicate node id %q", s.nodeID)
		return
	}
	b.seen[s.nodeID] = true
	if s.worker != nil {
		b.workers[s.node.Worker] = *s.worker
	}
	b.nodes = append(b.nodes, s.node)
	for _, p := range parents {
		b.edges = append(b.edges, domain.Edge{From: p, To: s.nodeID})
	}
	b.frontier = []string{s.nodeID}
}

// Worker adds a worker-backed node that depends on the current frontier (a
// linear step, or a fan-in if the frontier holds several nodes).
func (b *Builder) Worker(nodeID string, w domain.Worker) *Builder {
	b.add(Worker(nodeID, w), b.frontier)
	return b
}

// Tool adds a tool-backed node that depends on the current frontier.
func (b *Builder) Tool(nodeID string, tc domain.ToolCall) *Builder {
	b.add(Tool(nodeID, tc), b.frontier)
	return b
}

// Parallel adds several nodes that each depend on the current frontier, then
// sets the frontier to all of them — the next step (typically Merge) fans them
// back in.
func (b *Builder) Parallel(specs ...Spec) *Builder {
	if b.err != nil {
		return b
	}
	if len(specs) == 0 {
		b.err = fmt.Errorf("sdk: Parallel needs at least one node")
		return b
	}
	parents := b.frontier
	var next []string
	for _, s := range specs {
		b.add(s, parents)
		if b.err != nil {
			return b
		}
		next = append(next, s.nodeID)
	}
	b.frontier = next
	return b
}

// Merge adds a worker-backed node that fans in from explicit parents (or, if
// none are given, from the whole current frontier — the usual way to close a
// Parallel). The frontier advances to the merged node.
func (b *Builder) Merge(nodeID string, w domain.Worker, from ...string) *Builder {
	parents := from
	if len(parents) == 0 {
		parents = b.frontier
	}
	b.add(Worker(nodeID, w), parents)
	return b
}

// Build validates the accumulated graph and returns the compiled Workflow. The
// returned *Workflow both exposes the canonical domain.Workflow (for hashing or
// serialization) and carries the referenced Workers so it can Run itself.
func (b *Builder) Build() (*Workflow, error) {
	if b.err != nil {
		return nil, b.err
	}
	if len(b.nodes) == 0 {
		return nil, fmt.Errorf("sdk: workflow %q has no nodes", b.id)
	}
	// Non-nil empty slices marshal as [] (never null), matching how a YAML file
	// with `edges: []` loads — a prerequisite for byte-identical content hashes.
	edges := b.edges
	if edges == nil {
		edges = []domain.Edge{}
	}
	def := domain.Workflow{
		ID:      b.id,
		Version: b.version,
		Nodes:   b.nodes,
		Edges:   edges,
		Budget:  b.budget,
	}
	workers := make(map[string]domain.Worker, len(b.workers))
	for k, v := range b.workers {
		workers[k] = v
	}
	return &Workflow{def: def, workers: workers}, nil
}

// Workflow is a compiled SDK workflow: its canonical definition plus the Worker
// definitions its nodes reference (so it can assemble and Run itself).
type Workflow struct {
	def     domain.Workflow
	workers map[string]domain.Worker
}

// Definition returns the canonical domain.Workflow — the value that hashes and
// serializes identically to the equivalent YAML (REQ-SDK-01).
func (w *Workflow) Definition() domain.Workflow { return w.def }
