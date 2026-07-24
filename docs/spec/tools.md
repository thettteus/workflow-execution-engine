# Spec — Tool Interface & Built-in Tools

**Prefix:** `REQ-TOOL` · **Status:** STABLE (delivery M1.5; mutation classification M2.5) ·
**Principles:** PRIN-02, PRIN-04, PRIN-10 · **Implementation:** `core/tool/` (M1.5)

Workers invoke tools — git, filesystem, terminal, HTTP — through one simple interface. Nothing AI-specific.
Every call is schema-validated and audited; sandboxing is the default, not an option.

### REQ-TOOL-01 — Uniform, schema-validated tool calls
The engine shall invoke every tool through a single interface whose inputs and outputs are
schema-validated; an invalid call is rejected before execution.
- **Delivered by:** M1.5. **Verified by:** `tool.TestInvokeRejectsBadInputBeforeExecute`,
  `TestInvokeRejectsBadOutput`.

### REQ-TOOL-02 — Every call is an event pair
When a tool is invoked, the engine shall emit `ToolCalled` (tool, arguments) and `ToolResult` (outcome,
duration), making tool activity fully reconstructable from the log.
- **Rationale:** PRIN-02; tools are where workflows touch the world — the audit trail matters most here.
- **Delivered by:** M1.5 (`tool.Invoke`, unit-level), M1.6a (wired into the graph via `ToolExecutor`'s
  `ToolEmitter` capability — closing the M1.5 note that `tool.Invoke` was built and tested but never called
  from `core/engine`). **Verified by:** `tool.TestInvokeHappyPathEmitsEventPair`,
  `TestInvokePropagatesExecuteError` (error recorded on `ToolResult`); `engine.TestToolExecutorEmitsEventPair`
  (M1.6a, a real execution's log).

### REQ-TOOL-03 — Sandboxed by default (deny-first)
The engine shall scope the filesystem tool to the execution's working directory, gate the terminal tool
behind a per-workflow command allowlist, and gate the HTTP tool behind a per-workflow domain allowlist; a
request outside the allowlist fails the call with a distinct error — it is never silently attempted.
- **Rationale:** PRIN-10.
- **Delivered by:** M1.5. **Verified by:** `http.TestDisallowedDomainRejected` /
  `TestEmptyAllowlistDeniesAll`, `terminal.TestDisallowedCommandRejected`,
  `filesystem.TestPathTraversalRejected` / `TestSymlinkEscapeRejected`.

### REQ-TOOL-04 — Built-in set for the MVP
The engine shall ship filesystem, terminal, git, and HTTP tools (the set the flagship demo needs); custom
tools implement the same interface.
- **Delivered by:** M1.5 (filesystem, terminal, git, http). **Verified by:** per-tool test suites in
  `core/tool/*`; the flagship demo's Test Runner/Commit nodes wiring terminal/git as real graph nodes lands
  in `examples/` once the tool-backed executor exists (M1.6a); the M1.14 template gallery packages it.

The HTTP tool may apply declarative per-call URL rewrite rules before enforcing its domain allowlist, and
may fail the call on non-2xx responses when the workflow opts in. Domain-specific URL knowledge belongs in
workflow definitions, not in the HTTP tool implementation.

### REQ-TOOL-05 — Mutation classification before invocation
The built-in filesystem, terminal, git, and HTTP tools shall classify validated inputs as mutating or
read-only before invocation, so the runtime can persist an approval checkpoint before any local or remote
mutation. Filesystem writes, terminal commands, git add/commit/branch-create, and non-GET HTTP calls are
mutating; filesystem read/list, git status/diff/branch-list, and HTTP GET are read-only.
- **Delivered by:** M2.5. **Verified by:** per-tool `DescribeMutation` coverage through
  `engine.TestMutatingToolPausesBeforeToolCalledUntilApproved`,
  `TestUnattendedMutationOptInBypassesApproval`, and the existing built-in tool test suites.

### REQ-TOOL-06 — Structured JSON request bodies
The HTTP tool shall accept an optional `bodyJson` object input alongside its existing string `body`, so a
workflow can send a POST request whose body mixes static JSON structure with dynamic per-field values —
without a Worker hand-assembling and escaping an entire JSON document as one string. Each leaf value inside
`bodyJson` resolves through the same whole-string placeholder mechanism every other tool input already uses
(`resolveToolInput`, ADR 0008) — no new templating engine, no string concatenation. When present, `bodyJson`
is marshaled verbatim as the request body and the tool sets `Content-Type: application/json` unless the
workflow already supplied one.
- **Rationale:** closes a real gap found wiring `pr-review-autofix`'s forge-PR path (M2.5's Change Auto-Fix
  acceptance) — GitHub's Git Data API (blob/tree/commit/ref creation) needs a JSON body carrying a dynamic
  field (e.g. file content) next to static keys (e.g. `"encoding": "utf-8"`); the prior string-only `body`
  could only ever hold one whole-string placeholder, never a composed document.
- **Delivered by:** M2.5 follow-up. **Verified by:** `http.TestBodyJSONSentAsRequestBody`,
  `TestBodyJSONSetsContentTypeWhenAbsent`, `TestBodyJSONRespectsExplicitContentType`.

### REQ-TOOL-07 — Deterministic response-field chaining
When an HTTP tool call's response body itself parses as a JSON object or array, the engine shall expose it
already-parsed as an additional `bodyJson` output field (alongside the existing raw-string `body`), so a
downstream tool node can reference an exact field (`"${node-id.bodyJson.path}"`) through the same
placeholder walker every other tool input already uses — no Worker is asked to transcribe an exact token
(a commit sha, a blob sha) between two chained API calls, which is a real risk (a single mistranscribed
hex character invalidates the whole chain) a plain string-passing Worker could not reliably avoid.
`bodyJson` is absent whenever the body isn't a JSON object/array (plain text, HTML, a bare scalar) — `body`
remains the one field every caller can always depend on.
- **Rationale:** the same `pr-review-autofix` forge-PR path as REQ-TOOL-06 needs to chain GitHub's Git Data
  API (create blob → create tree → create commit → create ref) — each step's input is the *previous* step's
  server-assigned sha, known only after that call actually runs.
- **Delivered by:** M2.5 follow-up. **Verified by:** `http.TestBodyJSONResponseFieldIsParsed`,
  `TestBodyJSONResponseAbsentForNonJSONBody`.
