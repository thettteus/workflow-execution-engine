# Spec — Workers

**Prefix:** `REQ-WORKER` · **Status:** STABLE (delivery starts M1.4) · **Principles:** PRIN-03, PRIN-04,
PRIN-05 · **Implementation:** `core/domain/worker.go`, `core/engine/node.go` (executor seam), M1.4 onward

A Worker represents a **role** — reviewer, fixer, planner — not a persona and
not an "agent". <!-- vocab-allow: contrastive -->
A Worker is fully described by data: objective, constraints, allowed tools, context policy, output
contract, and model configuration. Workers are interchangeable: swapping the model or provider behind a
Worker changes cost and quality, never the workflow's shape.

### REQ-WORKER-01 — Worker as declarative role
The engine shall define a Worker entirely by its declared objective, constraints, tool allowlist, context
policy, output contract, and model configuration (`provider`, `model`, `params`) — no imperative code, no
hidden state.
- **Rationale:** PRIN-04; roles are reviewable data (versionable per REQ-VERSION-01).
- **Delivered by:** M1.1 (struct + schema), M1.4 (execution). **Verified by:** schema tests (M1.1);
  `engine.TestNoMalformedOutputCrossesBoundary` (M1.4 execution).

### REQ-WORKER-02 — Uniform executor boundary
The engine shall invoke every Worker through the single `NodeExecutor` boundary (`Execute(ctx, node,
inputs) → NodeResult`), so scheduling, retry, budgeting, caching, and event emission are identical for
model-backed, tool-backed, and stub executors.
- **Rationale:** PRIN-02 — one seam to observe; one seam to test against.
- **Delivered by:** M1.3 (seam), M1.4 (model-backed executor), M1.6a (tool-backed executor, closing this
  requirement's "tool-backed" clause). **Verified by:** scheduler tests run entirely through the seam
  (M1.3); `engine.WorkerExecutor` tests (M1.4); `engine.TestDispatchExecutorRoutesByNodeKind` (M1.6a).

### REQ-WORKER-03 — No malformed output crosses the boundary
The engine shall guarantee that no Worker output that fails its contract validation is ever visible to a
downstream node — enforcement happens inside the executor boundary, not as an optional post-step.
- **Rationale:** PRIN-08; this is what makes a Contract enforcement rather than suggestion.
- **Delivered by:** M1.4. **Verified by:** `engine.TestNoMalformedOutputCrossesBoundary`,
  `TestMalformedNeverReachesDownstream` (enforcement inside the `NodeExecutor` boundary).

### REQ-WORKER-04 — Tool-backed node declaration
A `Node` shall reference exactly one of a Worker (`worker`) or a Tool call (`tool: {toolName, input}`);
graph validation shall reject a node declaring both or neither. `Worker` itself is untouched — it still
means exactly what REQ-WORKER-01 says (an LLM role); a tool-backed node is a property of the `Node`, not a
new kind of Worker.
- **Rationale:** ADR 0008 — keeps the M1.6-frozen domain model's one reopening narrow (`domain.Node` only,
  not `domain.Worker`/`worker.schema.json`).
- **Delivered by:** M1.6a. **Verified by:** `validate.TestNodeRequiresExactlyOneOfWorkerOrTool` (both
  directions: neither set, both set).

### REQ-WORKER-05 — Tool-backed dispatch through the executor boundary
A `ToolExecutor` implementing `NodeExecutor` shall run a tool-backed node by resolving its `ToolCall`
against the tool registry and invoking it via `tool.Invoke` (REQ-TOOL-01), deterministically — no model
ever selects or shapes a tool call's input (ADR 0006). A `DispatchExecutor` shall compose model-backed and
tool-backed execution behind one `Scheduler.exec`, preserving REQ-WORKER-02's "identical seam" guarantee
for both kinds in the same graph.
- **Rationale:** ADR 0008; the flagship demo's Test Runner/Commit nodes need this to exist at all.
- **Delivered by:** M1.6a. **Verified by:** `engine.TestToolExecutorInvokesRegisteredTool`,
  `engine.TestDispatchExecutorRoutesByNodeKind`.

### REQ-WORKER-06 — Static artifact references and secret references in tool input
A tool-backed node's `Input` leaf value that is the *whole string* `${nodeID.path}` shall resolve to the
JSON value at `path` within the named upstream node's artifact (dotted-path lookup, no wildcards); a leaf
value that is the whole string `${env:NAME}` shall resolve to the OS environment variable `NAME` at call
time. Env-resolved values shall never appear in event payloads, returned error text, or the resulting
artifact content (NFR-SEC-01) — a tool's real output can legitimately echo back what it was given (e.g.
`curl -v`'s stderr), so the stored artifact needs the same redaction as the log. No
other placeholder syntax (embedded or concatenated within a larger string) is supported in v1 — a workflow
needing a composed multi-field string pushes that composition into a Worker's Contract output instead, and
the tool node references that one pre-composed field.
- **Rationale:** PRIN-05/ADR 0008 — reuses the existing dotted-path walker (no new query library, matching
  the project's rejection of `gjson`); NFR-SEC-01 extended to this new code path.
- **Delivered by:** M1.6a. **Verified by:** `engine.TestResolveToolInputArtifactReference`,
  `engine.TestResolveToolInputEnvReference`, `engine.TestToolExecutionRecordNeverContainsEnvSecretValue`.

### REQ-WORKER-07 — Tool-backed nodes are not cached (v1)
`ToolExecutor` shall not implement `CacheKeyer`; `DispatchExecutor.CacheKey` shall return `ok=false` for
any tool-backed node, unconditionally, regardless of whether its `Input` contains any placeholder.
- **Rationale:** a Tool is an opaque interface — the engine cannot generically verify its `Execute` doesn't
  read ambient state (e.g. a live working tree); REQ-CACHE-01's key-completeness guarantee cannot be
  honestly made for tool calls without a new, undelivered per-tool purity signal (ADR 0008).
- **Delivered by:** M1.6a. **Verified by:** `engine.TestDispatchExecutorCacheKeyOnlyForWorkerNodes`.

### REQ-WORKER-08 — Optional human-facing Worker description
A Worker may declare an optional `description`: human-facing text naming what the Worker is for, distinct
from `objective` (the behavioral instruction). `description` is part of the canonical Worker definition — it
participates in the content hash and versioning (REQ-VERSION-01..03), so changing it is a definition change
that requires a version bump — but the Contract compiler shall **not** include it in the compiled model
context, so it never reaches the model and never alters output or cache behavior. Being optional, it does not
change `worker.schema.json`'s `required` list.
- **Rationale:** the owner's ask that the product make each node's identity legible (name/version/description)
  alongside whether it is model-backed (REQ-UI-17..19); separates human-facing metadata from the behavioral
  `objective`. Recorded as a narrow, disclosed exception to the M1.6 domain freeze in
  [ADR 0017](../adr/0017-worker-description-field.md) (precedent
  [ADR 0008](../adr/0008-tool-backed-graph-nodes.md)).
- **Delivered by:** M2.12. **Verified by:** `domain.TestSchemaDrift`; `serialize.TestRoundTrip/worker`;
  `sdk.TestDescribeWorkerPreservesDescriptionInSDKDefinition`;
  `contract.TestCompileExcludesWorkerDescription`; `examples.TestFlagshipIsValid`.
