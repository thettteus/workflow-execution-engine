package engine

import (
	"context"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// Snapshot is the frozen graph + config written at ExecutionStarted and read
// back by Resume and core/replay. It is what makes an execution replayable
// (M1.7) or resumable (M1.3) without re-resolving anything live — exported so
// core/replay can read the exact type this package writes, instead of a
// hand-mirrored struct that could drift from it.
type Snapshot struct {
	Workflow    domain.Workflow `json:"workflow"`
	Budget      domain.Budget   `json:"budget"`
	Concurrency int             `json:"concurrency"`
	// DefinitionHashes pins the content hash of each worker "id@version" the run
	// used (REQ-VERSION-02). omitempty keeps a run that pinned nothing (e.g. a
	// bare test, or a run not driven by a registry) byte-identical to before.
	DefinitionHashes map[string]string `json:"definitionHashes,omitempty"`
	// Workers pins the full resolved Worker definition behind each
	// DefinitionHashes entry (REQ-UI-03) — see RunOptions.Workers. omitempty for
	// the same byte-identical-when-unused reason as DefinitionHashes.
	Workers map[string]domain.Worker `json:"workers,omitempty"`
	// Inputs records the actual resolved value (supplied or Default) behind
	// every declared InputDecl this run used (REQ-INPUT-01) — not a secret, so
	// unlike "${env:...}" values it belongs in the audit trail: "what was this
	// run actually run against". omitempty for the same byte-identical-when-
	// unused reason as DefinitionHashes/Workers.
	Inputs map[string]string `json:"inputs,omitempty"`
	// ConnectionRefs records the non-secret connection references resolved for
	// this run (REQ-CONN-06). Secret values are never present.
	ConnectionRefs map[string]ConnectionRef `json:"connectionRefs,omitempty"`
	// AllowUnattendedMutations records the explicit run-level opt-in, if used.
	AllowUnattendedMutations bool `json:"allowUnattendedMutations,omitempty"`
}

// Resume restarts an execution from its recorded state. It reads the snapshot
// and event log, treats every node with a recorded WorkerFinished as already
// done (reusing its persisted artifact), and runs the rest — so finished nodes
// are never re-executed. It appends to the same execution's log.
func (s *Scheduler) Resume(ctx context.Context, executionID string) (*Result, error) {
	snap, precompleted, err := s.reconstruct(executionID)
	if err != nil {
		return nil, err
	}
	return s.run(ctx, &snap.Workflow, resumeOpts(executionID, snap), precompleted, false)
}

// ResumeFrom is Resume, but node fromNodeID and every node reachable from it are
// forced to re-execute even if they previously finished; everything upstream is
// still reused from the record. This is the control plane's "retry from node"
// (REQ-CTRL-03, ADR 0012): re-run this node and its downstream, keep the rest.
// It pairs with a cache clear (or cache=off) when the intent is to genuinely
// recompute rather than serve the same cached artifact for an unchanged key.
func (s *Scheduler) ResumeFrom(ctx context.Context, executionID, fromNodeID string) (*Result, error) {
	snap, precompleted, err := s.reconstruct(executionID)
	if err != nil {
		return nil, err
	}
	if _, ok := nodeByID(&snap.Workflow, fromNodeID); !ok {
		return nil, fmt.Errorf("engine: resume-from %s: node %q not in workflow", executionID, fromNodeID)
	}
	for id := range descendantsInclusive(&snap.Workflow, fromNodeID) {
		delete(precompleted, id) // drop this node + its downstream so they re-run
	}
	return s.run(ctx, &snap.Workflow, resumeOpts(executionID, snap), precompleted, false)
}

// reconstruct reads an execution's frozen snapshot and rebuilds the set of nodes
// already completed on a prior run (those with a recorded WorkerFinished and a
// reloadable artifact) — the shared precondition for Resume and ResumeFrom.
func (s *Scheduler) reconstruct(executionID string) (Snapshot, map[string]nodeOutput, error) {
	var snap Snapshot
	if err := s.log.ReadSnapshot(executionID, &snap); err != nil {
		return Snapshot{}, nil, fmt.Errorf("engine: resume %s: %w", executionID, err)
	}
	events, err := s.log.ReadAll(executionID)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("engine: resume %s: %w", executionID, err)
	}

	// A node is done iff it recorded a WorkerFinished; its artifact hash comes
	// from the ArtifactCreated that precedes it.
	hashByNode := make(map[string]string)
	typeByNode := make(map[string]domain.ArtifactType)
	finished := make(map[string]bool)
	for _, ev := range events {
		switch ev.Type {
		case domain.ArtifactCreated:
			if h, ok := ev.Payload["hash"].(string); ok {
				hashByNode[ev.NodeID] = h
			}
			if t, ok := ev.Payload["type"].(string); ok {
				typeByNode[ev.NodeID] = domain.ArtifactType(t)
			}
		case domain.WorkerFinished:
			finished[ev.NodeID] = true
		}
	}

	precompleted := make(map[string]nodeOutput, len(finished))
	for nodeID := range finished {
		hash := hashByNode[nodeID]
		if hash == "" {
			// Finished without a recorded artifact — cannot reuse it; let it re-run.
			continue
		}
		content, err := s.store.Get(hash)
		if err != nil {
			return Snapshot{}, nil, fmt.Errorf("engine: resume %s: reload artifact for node %q: %w", executionID, nodeID, err)
		}
		precompleted[nodeID] = nodeOutput{Hash: hash, Content: content, Type: typeByNode[nodeID]}
	}
	return snap, precompleted, nil
}

// resumeOpts rebuilds the RunOptions a resumed run needs from the frozen
// snapshot — concurrency, budget, inputs, and the definition/worker pins — so a
// resumed run's remaining nodes see exactly what the original run resolved.
func resumeOpts(executionID string, snap Snapshot) RunOptions {
	return RunOptions{
		ExecutionID:              executionID,
		Concurrency:              snap.Concurrency,
		Budget:                   snap.Budget,
		Inputs:                   snap.Inputs,
		DefinitionHashes:         snap.DefinitionHashes,
		Workers:                  snap.Workers,
		ConnectionRefs:           snap.ConnectionRefs,
		AllowUnattendedMutations: snap.AllowUnattendedMutations,
	}
}

// nodeByID finds a node in wf by id.
func nodeByID(wf *domain.Workflow, id string) (domain.Node, bool) {
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return domain.Node{}, false
}

// descendantsInclusive returns start plus every node reachable from it by
// following edges forward (start's transitive downstream). Used by ResumeFrom to
// decide which recorded nodes to invalidate. Robust to cycles via a visited set.
func descendantsInclusive(wf *domain.Workflow, start string) map[string]bool {
	children := make(map[string][]string)
	for _, e := range wf.Edges {
		children[e.From] = append(children[e.From], e.To)
	}
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, children[id]...)
	}
	return seen
}
