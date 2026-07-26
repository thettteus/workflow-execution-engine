package engine

import (
	"context"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// DispatchExecutor composes a model-backed WorkerExecutor and a tool-backed
// ToolExecutor behind Scheduler's single exec field (ADR 0008), routing each
// node on whether it declares a Tool or a Worker. This is what lets one graph
// mix LLM-backed nodes (Reviewer, Fixer) and deterministic tool nodes (Test
// Runner, Commit) while preserving REQ-WORKER-02's "identical seam" guarantee
// for both — Scheduler itself never changes; only the concrete executor
// behind it composes.
type DispatchExecutor struct {
	workers *WorkerExecutor
	tools   *ToolExecutor
}

// NewDispatchExecutor builds a DispatchExecutor over both underlying
// executors.
func NewDispatchExecutor(workers *WorkerExecutor, tools *ToolExecutor) *DispatchExecutor {
	return &DispatchExecutor{workers: workers, tools: tools}
}

var (
	_ NodeExecutor = (*DispatchExecutor)(nil)
	_ ToolEmitter  = (*DispatchExecutor)(nil)
	_ CacheKeyer   = (*DispatchExecutor)(nil)
)

// Execute implements NodeExecutor.
func (d *DispatchExecutor) Execute(ctx context.Context, req NodeRequest) (NodeResult, error) {
	if req.Node.Tool != nil {
		return d.tools.Execute(ctx, req)
	}
	return d.workers.Execute(ctx, req)
}

// ExecuteWithEmit implements ToolEmitter. Worker-backed nodes don't need the
// emit bridge — WorkerExecutor doesn't implement ToolEmitter itself — so they
// fall back to the plain Execute path; only tool-backed nodes route through
// the emit-carrying call.
func (d *DispatchExecutor) ExecuteWithEmit(ctx context.Context, req NodeRequest, emit func(domain.EventType, map[string]any)) (NodeResult, error) {
	if req.Node.Tool != nil {
		return d.tools.ExecuteWithEmit(ctx, req, emit)
	}
	return d.workers.Execute(ctx, req)
}

// CacheKey implements CacheKeyer. Tool-backed nodes never cache
// (REQ-WORKER-07, ADR 0008) — a Tool is opaque to the engine, which cannot
// verify its Execute doesn't read ambient state.
func (d *DispatchExecutor) CacheKey(node domain.Node, inputs []NodeInput, workflowInputs map[string]string) (string, bool) {
	if node.Tool != nil {
		return "", false
	}
	return d.workers.CacheKey(node, inputs, workflowInputs)
}
