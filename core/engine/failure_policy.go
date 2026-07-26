package engine

import "github.com/thettteus/workflow-execution-engine/core/domain"

// failurePolicyOf returns a node's failure policy, defaulting to fail-execution
// (a node failure halts the whole execution) when the node declares none.
//
// The scheduler acts on the returned mode:
//
//   - fail-execution: halt — stop dispatching, cancel in-flight work, the
//     execution ends failed.
//   - continue: mark the node failed and keep going; edges out of it become
//     inactive, so only its dependent branch is skipped, not siblings.
//   - fallback-node: run FallbackNode in the failed node's place, attributing
//     its output to the failed node so downstream edges resolve.
func failurePolicyOf(node domain.Node) domain.FailurePolicy {
	if node.OnFailure != nil && node.OnFailure.Mode != "" {
		return *node.OnFailure
	}
	return domain.FailurePolicy{Mode: domain.FailExecution}
}
