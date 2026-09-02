# Spec — Contracts

**Prefix:** `REQ-CONTRACT` · **Status:** STABLE (delivery M1.4) · **Principles:** PRIN-01, PRIN-05,
PRIN-08 · **Implementation:** `core/contract/` (M1.4)

Workers execute Contracts. A Contract defines the goal, rules, constraints, success criteria, and the
**required output schema** (JSON Schema, draft 2020-12). Contracts are **enforced, not suggested** — a
contract without enforcement is just a prompt with a different name. <!-- vocab-allow: contrastive -->
Message construction from a Contract is internal plumbing (`core/contract/compiler.go`), never a
user-facing concept (PRIN-04).

### REQ-CONTRACT-01 — Output conforms before propagation
When a Worker produces output, the engine shall parse it as JSON and validate it against the node's
`contract.outputSchema` (via `core/validate`) before any downstream node may consume it.
- **Rationale:** PRIN-01 (recorded, typed results), PRIN-08.
- **Delivered by:** M1.4. **Verified by:** `engine.TestNoMalformedOutputCrossesBoundary`,
  `TestMalformedNeverReachesDownstream` (`outputSchema = {score, issues[]}`).

### REQ-CONTRACT-02 — Bounded retry with delta feedback
If output validation fails, then the engine shall re-invoke the Worker with the validation errors appended
as feedback — **only the errors, never a re-inflated copy of the full context** — at most
`contract.maxRetries` times, emitting a `Retry` event (carrying the validation error text) per attempt.
- **Rationale:** PRIN-05 (feedback is delta, not re-inflation) — this is the token-economy rule applied to
  the engine's own loop.
- **Delivered by:** M1.4. **Verified by:** `engine.TestContractRetryWithDeltaFeedback` (malformed-once-then-valid
  → exactly one `Retry` event carrying the validation text; retry call carries the delta and only the delta).

### REQ-CONTRACT-03 — Explicit terminal violation
If validation still fails after `contract.maxRetries` attempts, then the engine shall emit
`ContractViolation` and fail the node under its failure policy (REQ-RUNTIME-04) — never silently pass the
malformed output through.
- **Delivered by:** M1.4. **Verified by:** `engine.TestContractViolationTerminal` (emits `ContractViolation`,
  no `ContractValidated`, run fails).

### REQ-CONTRACT-04 — Tight-by-default schema guidance (anti-slop)
The engine's documentation and templates shall model tight contracts — bounded arrays (`maxItems`), bounded
strings (`maxLength`), enums over free prose — and the flagship templates shall use them.
- **Rationale:** PRIN-08 — slop needs unbounded space; contracts deny it.
- **Delivered by:** M1.4 (examples), M1.14 (templates). **Verified by:** review of shipped templates
  (_pending_).

### REQ-CONTRACT-05 — Verifier-node pattern
The project shall document and include in its source examples the verification pattern: a cheap verifier
Worker judging an expensive producer Worker's artifact against objective criteria, gating the graph via a
conditional edge (REQ-RUNTIME-02). A mutating verifier workflow shall enter the published gallery only
after REQ-RUNTIME-07 and REQ-UI-06 are delivered.
- **Rationale:** PRIN-08 — verification is a graph shape, not a hope; composes from existing primitives.
- **Delivered by:** M1.14 (template), docs in M1.15. **Verified by:** `examples/bug-investigation` —
  `verify-patch` (a distinct Worker, gpt-4o-mini) judges `patch` (the producer, gpt-4o) against objective
  criteria, gating `apply-patch` via a conditional edge; locked in by
  `examples/examples_test.go`'s `TestBugInvestigationHasVerifierNode` (asserts the gate's `From` is the
  judge, not the producer). It remains a source example until the human-approval requirements land.
