# Architecture — Workflow Execution Engine

> Non-normative, like [VISION.md](VISION.md), whose "Architecture at a glance" section this expands into
> diagrams. This is the **as-built and as-planned map of the system**, kept current as milestones land —
> when it drifts from `core/`, trust the code and fix this file in the same commit. The binding boundaries
> (Go vs TypeScript, one provider interface, no vendor SDKs, hash-chained log) are ADRs; this file just
> draws them.

## Component map

Two languages, one boundary — Go below the event stream, TypeScript only in `ui/` (ADR 0002). Nodes marked
`✅` exist today; `▢` are specified but not yet built, tagged with the milestone that delivers them. Phase 1
(M1.0–M1.15) is complete; Phase 2 hardening is in progress (see [ROADMAP.md](ROADMAP.md) and
[EXECUTION-PHASE2.md](EXECUTION-PHASE2.md) for the authoritative milestone-by-milestone status) — this
diagram includes what Phase 2 has added to the component map so far.

```mermaid
graph TB
    subgraph Authoring["Authoring — three doors, one canonical form (ADR 0003)"]
        YAML["YAML / JSON workflow definition"]
        SDKCode["Go code (sdk/) ✅ M1.10"]
    end

    subgraph Schemas["schemas/ ✅ — JSON Schema draft 2020-12, language-neutral source of truth"]
        SchemaFiles["workflow · worker · contract · context-policy · artifact · event · execution · budget"]
    end

    subgraph Core["core/ — Go, single module, single static binary"]
        Domain["domain ✅ — structs mirroring schemas/"]
        Serialize["serialize ✅ — YAML ⇄ JSON ⇄ struct, loss-free"]
        Canonical["canonical ✅ — deterministic JSON + SHA-256 (ADR 0004)"]
        Validate["validate ✅ — schema validation, positional errors (ADR 0005)"]
        Engine["engine ✅ — scheduler: parallel dispatch, conditional edges,\nretry classes, failure policies, cancellation, resume, budget halt"]
        Contract["contract ✅ M1.4 — compiles Contract → Worker call,\nvalidates output, delta-feedback retry"]
        Policy["policy ✅ M1.4 — resolves each Worker's context slice"]
        Model["model/openai, model/anthropic ✅ M1.4 —\nhand-rolled net/http clients (ADR 0006)"]
        Cost["cost ✅ M1.4 — per-call token/$ accounting"]
        Tool["tool ✅ M1.5 — filesystem, terminal, git, http (sandboxed, PRIN-10);\ntool-backed graph nodes ✅ M1.6a (ADR 0008); structured JSON request/\nresponse bodies ✅ (REQ-TOOL-06/07)"]
        Diagnostic["diagnostic ✅ M2.4 — structured error taxonomy:\nevery failure names its node, kind, and likely fix"]
        Security["security ✅ M2.4 — secret/API-key scanning across\nevents, artifacts, cache, and exports"]
        Cache["cache ✅ M1.6 — content-addressed node cache"]
        Replay["replay ✅ M1.7 — zero-cost audit replay + re-execution + divergence"]
        Registry["registry ✅ M1.8 — immutable versioned definitions,\nWorkerSource + hash-pinning + portable export"]
        Store["store ✅ — content-addressed artifact store"]
        EventLog["eventlog ✅ — append-only JSONL, hash-chained (ADR 0007)"]
        Settings["settings ✅ M2.2/M2.9 — durable non-secret settings\n+ Connections (model providers, change sources)"]
        Server["server ✅ M1.12 — HTTP + WebSocket (ADR 0010, coder/websocket);\ntails the log, lists/audits executions, starts/cancels/resumes runs ✅ M2.2;\ngates mutating tool calls behind an approval checkpoint ✅ M2.5 (ADR 0016)"]
    end

    subgraph Clients["Clients — no second source of truth"]
        CLI["cli/ ✅ M1.9 — wee binary (run/replay/inspect/validate/export/cache/init/list/serve)"]
        SDKPkg["sdk/ ✅ M1.10 — Go authoring SDK, in-process (builder + Run + typed artifacts)"]
        UI["ui/ ✅ M1.11 — React + TypeScript visual builder;\nlive event stream ✅ M1.12, inspector/artifacts ✅ M1.13;\nconnections/theme/notifications ✅ M2.9–M2.11"]
    end

    YAML --> Serialize
    SDKCode --> SDKPkg --> Domain
    SchemaFiles -.validates.-> Validate
    SchemaFiles -.mirrors.-> Domain
    Serialize --> Domain
    Domain --> Validate --> Engine
    Domain --> Canonical

    Engine --> Contract --> Validate
    Engine --> Policy
    Engine --> Model
    Engine --> Tool
    Engine --> Cache
    Engine --> Cost
    Engine --> Store
    Engine --> EventLog
    Engine --> Diagnostic
    Security -.scans.-> EventLog
    Security -.scans.-> Store
    Cache -.keys via.-> Canonical
    Store -.addresses via.-> Canonical
    Registry --> Engine

    EventLog --> Replay
    Store --> Replay
    Replay --> Cache

    CLI --> Engine
    CLI --> Replay
    CLI --> Server
    EventLog -->|"--json"| CLI
    EventLog --> Server
    Server --> Settings
    Server -->|"WebSocket (ADR 0010)"| UI
```

## Execution lifecycle (single node)

What happens once the engine dispatches a Worker — the loop that PRIN-05 (token economy) and PRIN-08
(anti-slop) are enforced inside:

```mermaid
sequenceDiagram
    participant Engine as engine (scheduler)
    participant Policy as policy (context)
    participant Model as model/<provider>
    participant Contract as contract (validate)
    participant Cache as cache
    participant Store as store (artifacts)
    participant Log as eventlog

    Engine->>Cache: cache key ready? (Worker+Contract version, inputs, model params)
    alt cache hit
        Cache-->>Engine: cached artifact
        Engine->>Log: CacheHit
    else cache miss
        Engine->>Log: CacheMiss
        Engine->>Policy: resolve declared context policy
        Policy-->>Engine: minimal slice (never full history by default)
        Engine->>Model: Complete(ctx, messages, params)
        Model-->>Engine: response + token usage
        Engine->>Contract: validate output against outputSchema
        alt invalid
            Contract-->>Engine: validation errors (delta, not re-inflated context)
            Engine->>Model: retry with errors appended (bounded by maxRetries)
            Engine->>Log: Retry
        else valid
            Contract-->>Engine: ok
            Engine->>Store: put artifact (content-addressed)
            Engine->>Log: ContractValidated, ArtifactCreated
        end
    end
    Engine->>Engine: check budget before next dispatch
    Engine->>Log: WorkerFinished
```

## Reading this diagram against the specs

| Diagram element | Governing spec |
|---|---|
| Parallel dispatch, conditional edges, retry classes, failure policy, cancellation, resume | [spec/runtime.md](spec/runtime.md) |
| `contract` compile/validate/retry loop | [spec/contracts.md](spec/contracts.md) |
| `policy` context slicing | [spec/context-policies.md](spec/context-policies.md) |
| `model/*` provider clients | [spec/model-providers.md](spec/model-providers.md), [ADR 0006](adr/0006-model-provider-integration.md) |
| `cache` key + hit/miss/invalidation | [spec/cache.md](spec/cache.md) |
| `cost` accounting | [spec/budgets.md](spec/budgets.md) (REQ-BUDGET-03) |
| `tool` sandboxing | [spec/tools.md](spec/tools.md), [spec/security.md](spec/security.md) |
| `store` / content addressing | [spec/artifacts.md](spec/artifacts.md), [ADR 0004](adr/0004-content-addressing.md) |
| `eventlog` catalog + hash chain | [spec/events.md](spec/events.md), [ADR 0007](adr/0007-event-log-hash-chain.md) |
| `replay` | [spec/replay.md](spec/replay.md) |
| `registry` / versioning | [spec/versioning.md](spec/versioning.md) |
| `cli/`, `sdk/`, `ui/` | [spec/cli.md](spec/cli.md), [spec/sdk.md](spec/sdk.md), [spec/ui.md](spec/ui.md) |
| `server` live event transport (WebSocket, `coder/websocket`) | [spec/ui.md](spec/ui.md) REQ-UI-02, [ADR 0010](adr/0010-websocket-transport.md) |
| `diagnostic` structured error taxonomy | [spec/runtime.md](spec/runtime.md) |
| `security` secret/API-key scanning and redaction | [spec/security.md](spec/security.md) |
| `settings` durable non-secret settings + Connections | [spec/connections.md](spec/connections.md), [spec/control-plane.md](spec/control-plane.md), [ADR 0012](adr/0012-local-control-plane.md), [ADR 0013](adr/0013-connections-model.md) |
| Safe-mutation approval checkpoints (pause before a mutating tool call) | [ADR 0016](adr/0016-safe-mutation-approval-checkpoints.md) |

## The flagship graph, for scale

The component map above is the engine; the shape it's built to run well is the flagship demo — see
[VISION.md → Flagship Demo](VISION.md#flagship-demo) for the conceptual shape (parallel reviewers feeding a
Fixer → Test Runner → Commit) and
[examples/pr-review-autofix/README.md](../examples/pr-review-autofix/README.md) for the concrete, currently
implemented graph: 3 parallel reviewers plus a `locate-file`/`read-original` pair that hands the Fixer the
real base file, a `fixer` whose own output is judged by a separate `verify-fix` Worker before anything is
applied, a local `apply-fix` → `test` → `stage` → `commit` tail, and — landing the accepted fix on GitHub
for real — a `push-plan` Worker plus a `checkout-branch`/`get-base-commit`/`create-blob`/`create-tree`/
`create-commit`/`create-branch`/`open-pr` tail that speaks GitHub's Git Data API directly through the `http`
tool (REQ-TOOL-06/07), since `core/tool/git` deliberately never reaches a remote. 21 nodes total (7 model /
14 deterministic). That graph exercises every box above at once: parallel dispatch, context policies,
contract enforcement, cache, budget, approval checkpoints, and the event stream the UI renders live.
