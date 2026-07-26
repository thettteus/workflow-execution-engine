package replay

import (
	"context"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
)

// Reexecuter re-runs a previously executed workflow under its frozen
// configuration (REQ-REPLAY-02). It is a thin wrapper around the same
// Scheduler an original run used: identical graph, budget, and concurrency,
// so the node cache (REQ-CACHE-02) reuses every node whose key still
// matches, and only genuinely invalidated nodes reach a model or a tool.
type Reexecuter struct {
	log       *eventlog.Log
	scheduler *engine.Scheduler
}

// NewReexecuter builds a Reexecuter over the event log an original run wrote
// its snapshot to, and the Scheduler to run the reconstructed workflow
// through. The Scheduler must share the original run's artifact store and
// cache for REQ-REPLAY-02's cache reuse to apply.
func NewReexecuter(log *eventlog.Log, scheduler *engine.Scheduler) *Reexecuter {
	return &Reexecuter{log: log, scheduler: scheduler}
}

// Reexecute loads originalExecutionID's frozen snapshot — the exact
// workflow, budget, and concurrency it ran with — and runs it again as
// newExecutionID. Nothing is re-resolved live: the graph that runs is
// byte-identical to the one that was recorded, per REQ-REPLAY-02.
func (r *Reexecuter) Reexecute(ctx context.Context, originalExecutionID, newExecutionID string) (*engine.Result, error) {
	var snap engine.Snapshot
	if err := r.log.ReadSnapshot(originalExecutionID, &snap); err != nil {
		return nil, fmt.Errorf("replay: reexecute %s: %w", originalExecutionID, err)
	}
	opts := engine.RunOptions{
		ExecutionID: newExecutionID,
		Concurrency: snap.Concurrency,
		Budget:      snap.Budget,
	}
	return r.scheduler.Run(ctx, &snap.Workflow, opts)
}
