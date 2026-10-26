package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/contract"
	"github.com/thettteus/workflow-execution-engine/core/cost"
	"github.com/thettteus/workflow-execution-engine/core/diagnostic"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/policy"
)

// WorkerSource resolves a node's "worker" reference (id@version) to its Worker
// definition. Until the registry lands (M1.8) this is any in-memory map; the
// registry will satisfy the same interface without the executor changing.
type WorkerSource interface {
	Lookup(ref string) (domain.Worker, bool)
}

// MapWorkerSource is a WorkerSource backed by a map keyed by "id@version".
type MapWorkerSource map[string]domain.Worker

// Lookup implements WorkerSource.
func (m MapWorkerSource) Lookup(ref string) (domain.Worker, bool) { w, ok := m[ref]; return w, ok }

// WorkerExecutor is the model-backed NodeExecutor (REQ-WORKER-02): it resolves
// the Worker, narrows context to what the policy admits (REQ-CTXPOL-01),
// compiles the one model call (REQ-CONTRACT-01 plumbing), invokes the selected
// provider (REQ-MODEL-01), enforces the output contract (REQ-CONTRACT-01..03),
// and prices the call (REQ-BUDGET-03). It holds no per-node or cross-attempt
// state — retry feedback arrives through NodeRequest, and the engine owns the
// retry loop.
type WorkerExecutor struct {
	workers   WorkerSource
	providers *model.Registry
}

// NewWorkerExecutor builds a WorkerExecutor over a worker source and a provider
// registry.
func NewWorkerExecutor(workers WorkerSource, providers *model.Registry) *WorkerExecutor {
	return &WorkerExecutor{workers: workers, providers: providers}
}

// Execute implements NodeExecutor.
func (e *WorkerExecutor) Execute(ctx context.Context, req NodeRequest) (NodeResult, error) {
	w, ok := e.workers.Lookup(req.Node.Worker)
	if !ok {
		err := fmt.Errorf("engine: no worker %q for node %q", req.Node.Worker, req.Node.ID)
		return NodeResult{}, Fatal(diagnostic.Wrap(err, diagnostic.KindValidation, "worker_not_found", req.Node.ID, "worker.lookup", "worker reference could not be resolved", "select an existing Worker version or import the missing Worker"))
	}

	// Resolve exactly what this Worker may see. A node-level policy overrides the
	// Worker's; absent both, Resolve defaults to parent-only (REQ-CTXPOL-02).
	pol := w.ContextPolicy
	if req.Node.ContextPolicy != nil {
		pol = *req.Node.ContextPolicy
	}
	admitted, err := policy.Resolve(pol, toPolicyItems(req.Inputs))
	if err != nil {
		err = fmt.Errorf("engine: node %q: %w", req.Node.ID, err)
		return NodeResult{}, Fatal(diagnostic.Wrap(err, diagnostic.KindValidation, "context_policy_invalid", req.Node.ID, "context.resolve", "context policy could not be resolved", "check the node's Context Policy filters and upstream artifact types"))
	}

	messages := contract.Compile(w, admitted, req.RetryFeedback)

	prov, err := e.providers.Get(w.Model.Provider)
	if err != nil {
		err = fmt.Errorf("engine: node %q: %w", req.Node.ID, err)
		return NodeResult{}, Fatal(diagnostic.Wrap(err, diagnostic.KindProvider, "provider_not_configured", req.Node.ID, "provider.lookup", "model provider is not configured", "add or select a provider connection in Settings"))
	}

	resp, err := prov.Complete(ctx, messages, model.Params{Model: w.Model.Model, Extra: w.Model.Params})
	if err != nil {
		return NodeResult{}, mapProviderError(req.Node.ID, err)
	}

	// The call is billed whether or not its output clears the contract, so the
	// spend is computed before enforcement and carried on a rejection too.
	providerName := w.Model.Provider
	if providerName == "" {
		providerName = model.DefaultProvider
	}
	callCost := cost.Compute(providerName, w.Model.Model, resp.InputTokens, resp.OutputTokens)
	callTokens := resp.InputTokens + resp.OutputTokens

	output := []byte(resp.Content)
	if verr := contract.Enforce(w.Contract, output); verr != nil {
		var ve *contract.ViolationError
		if errors.As(verr, &ve) {
			// Retryable with delta feedback, bounded by contract.maxRetries.
			err = diagnostic.Wrap(ve, diagnostic.KindValidation, "contract_violation", req.Node.ID, "contract.enforce", "output failed the node's Contract", "adjust the Worker goal or output schema so the model returns valid JSON")
			return NodeResult{}, WithSpend(ContractViolation(err, ve.Feedback, w.Contract.MaxRetries), callCost, callTokens)
		}
		// A malformed outputSchema is a configuration fault, not a violation.
		err = fmt.Errorf("engine: node %q: %w", req.Node.ID, verr)
		return NodeResult{}, WithSpend(Fatal(diagnostic.Wrap(err, diagnostic.KindValidation, "contract_schema_invalid", req.Node.ID, "contract.compile", "Contract output schema is invalid", "fix the Worker Contract schema before running")), callCost, callTokens)
	}

	return NodeResult{
		Content:       output,
		Type:          domain.ArtifactJSON,
		MimeType:      "application/json",
		CostUSD:       callCost,
		Tokens:        callTokens,
		Validated:     true,
		ContextHashes: policy.Hashes(admitted),
	}, nil
}

// CacheKey derives the node's cache key from its Worker definition and resolved
// inputs (REQ-CACHE-01), opting model-backed nodes into the cache. It returns
// ok=false when the Worker can't be resolved (the node then always executes).
//
// Tool "versions" are the Worker's declared tool names for now: tools are not
// yet invoked by the executor (that wiring lands with the flagship template,
// M1.14), so the names are the version proxy — changing the allowlist still
// invalidates the key.
func (e *WorkerExecutor) CacheKey(node domain.Node, inputs []NodeInput, workflowInputs map[string]string) (string, bool) {
	w, ok := e.workers.Lookup(node.Worker)
	if !ok {
		return "", false
	}
	contractHash, err := canonical.Hash(w.Contract)
	if err != nil {
		return "", false
	}
	hashes := make([]string, 0, len(inputs))
	for _, in := range inputs {
		hashes = append(hashes, in.Hash)
	}
	pol := w.ContextPolicy
	if node.ContextPolicy != nil {
		pol = *node.ContextPolicy
	}
	return cache.Key(cache.Inputs{
		WorkerID:            w.ID,
		WorkerVersion:       w.Version,
		ContractHash:        contractHash,
		InputArtifactHashes: hashes,
		WorkflowInputs:      workflowInputs,
		Model:               w.Model,
		ToolVersions:        w.Tools,
		ContextPolicy:       pol,
	}), true
}

// mapProviderError translates a provider's transient/fatal classification into
// the engine's retry classes (REQ-MODEL-05). Anything unrecognized is fatal.
func mapProviderError(nodeID string, err error) error {
	var te *model.TransientError
	if errors.As(err, &te) {
		wrapped := diagnostic.Wrap(err, diagnostic.KindProvider, "provider_transient", nodeID, "provider.complete", "provider returned a retryable failure", "wait for the retry window or lower concurrency/rate")
		if te.HasRetry {
			wrapped = diagnostic.WithRetryAfter(wrapped, te.RetryAfter)
		}
		return Transient(wrapped)
	}
	var fe *model.FatalError
	if errors.As(err, &fe) {
		return Fatal(diagnostic.Wrap(err, diagnostic.KindProvider, "provider_fatal", nodeID, "provider.complete", "provider rejected the request", "check provider credentials, base URL, model name, and request parameters"))
	}
	return Fatal(diagnostic.Wrap(err, diagnostic.KindProvider, "provider_unknown", nodeID, "provider.complete", "provider call failed", "check provider configuration and network access"))
}

func toPolicyItems(inputs []NodeInput) []policy.Item {
	items := make([]policy.Item, len(inputs))
	for i, in := range inputs {
		items[i] = policy.Item{FromNode: in.FromNode, Type: in.Type, Hash: in.Hash, Content: in.Content}
	}
	return items
}
