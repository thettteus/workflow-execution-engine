package sdk

import (
	"encoding/json"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// Artifact returns a node's output artifact decoded into T (REQ-SDK-02). It
// waits for the run to finish, finds the node's stored artifact by hash, and
// unmarshals it. The artifact was already validated against the node's Contract
// before it was stored (REQ-WORKER-03) — so a worker node's output is
// guaranteed to satisfy that schema; Artifact[T] adds the caller's Go type on
// top. A node that failed, was skipped, or produced no artifact is an error.
func Artifact[T any](e *Execution, nodeID string) (T, error) {
	var zero T
	if _, err := e.Wait(); err != nil {
		// The run failed overall; a specific node may still have an artifact, so
		// don't bail here — fall through and report per-node below.
		_ = err
	}

	e.mu.Lock()
	res := e.result
	e.mu.Unlock()
	if res == nil {
		return zero, fmt.Errorf("sdk: execution %s produced no result", e.ID)
	}
	outcome, ok := res.Nodes[nodeID]
	if !ok {
		return zero, fmt.Errorf("sdk: no node %q in execution %s", nodeID, e.ID)
	}
	if outcome.State != engine.StateSucceeded || outcome.Hash == "" {
		return zero, fmt.Errorf("sdk: node %q did not produce an artifact (state %s)", nodeID, outcome.State)
	}

	content, err := e.store.Get(outcome.Hash)
	if err != nil {
		return zero, fmt.Errorf("sdk: load artifact for node %q: %w", nodeID, err)
	}
	var out T
	if err := json.Unmarshal(content, &out); err != nil {
		return zero, fmt.Errorf("sdk: decode node %q artifact into %T: %w", nodeID, out, err)
	}
	return out, nil
}
