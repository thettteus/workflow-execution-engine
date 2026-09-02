# Replay honesty

**Requirement:** REQ-REPLAY-01..03 · **Status:** v1 (M1.7) · **Principles:** PRIN-01, PRIN-05 ·
**Implementation:** `core/replay/`.

Replay is two distinct operations, never conflated (REQ-REPLAY-03): **audit** inspects a recorded execution;
**re-execution** runs the process again. This document states plainly what each one guarantees, and what it
does not — the CLI (M1.9) and any future UI must present both this way, not blur them into one "replay"
button.

## Audit — a read, not a re-run

`replay.Auditor.Audit(executionID)` renders a `Timeline` from `snapshot.json` + `events.jsonl` + the
artifact store alone. It holds no reference to a `Scheduler` or a `NodeExecutor` — structurally, it cannot
call a model or a tool.

**Guaranteed:**
- Zero model calls, zero tool calls, zero cost.
- The workflow graph, the full ordered event timeline, and every node's actual artifact bytes are exactly
  what was recorded — byte-identical to the original run, always.
- A node with no recorded event is reported `Skipped` if the execution reached `ExecutionFinished` (it was
  never on the path a conditional edge or a fallback policy took), or `Pending` otherwise (the run stopped
  before reaching it — a crash, a cancellation, or a still-in-flight execution).

**Not guaranteed:**
- Audit cannot tell you *why* a conditional edge evaluated the way it did beyond what the event log already
  recorded — it replays the record, it does not re-evaluate the condition.
- A node that was `ready` but never dispatched before the run halted (e.g. it lost a race against a budget
  cutoff) looks identical, from the log alone, to a node the graph never intended to reach. Both report
  `Skipped`/`Pending` by the same rule above; distinguishing "never scheduled" from "skipped by design"
  would need a new recorded fact this milestone doesn't add, since no requirement asks for it yet.

## Re-execution — the same process, run again

`replay.Reexecuter.Reexecute(ctx, originalID, newID)` loads the original's frozen `Snapshot` — the exact
workflow graph, budget, and concurrency it ran with — and runs it again under a new execution ID, through
the same `Scheduler`. Nothing about the graph is re-resolved live.

**Guaranteed:**
- The graph, budget, and concurrency are byte-identical to the original run's.
- A node whose cache key (REQ-CACHE-01) still matches reuses its prior artifact at zero cost
  (REQ-CACHE-02) — this is what makes re-execution cheap, not a replay-specific optimization.
- Only nodes whose key has actually changed since the original run reach a model or a tool again. A node's
  key can change even though the *graph* didn't: a Worker's Contract or model config resolved from a
  registry can be edited between the original run and now (M1.8 pins definitions to content hashes to make
  this auditable; M1.7 alone already makes the cache key sensitive to it).

**Not guaranteed — the one that matters most:**
- **A re-executed node's output is not guaranteed to match its original bytes.** LLM-backed Workers are not
  deterministic. Two calls with the identical context, contract, and model config can legitimately produce
  different (even equally valid) output. Re-execution replays the *process*; it does not replay the
  *result*. A tool-backed node (M1.6a) is not cached at all in v1 (REQ-WORKER-07) — every re-execution
  reruns it, and whether *that* is byte-identical depends entirely on the tool (`git diff` against a
  changed working tree will differ; a pure computation will not).

## Divergence — the honest comparison

`replay.Divergence(original, reexecuted)` compares two `Timeline`s node by node:

| Status | Meaning |
|---|---|
| `Cached` | Both recorded the exact same artifact hash. Nothing new happened here. |
| `ReExecuted` | Both have the node, but the hash differs. It ran again and produced different output — this is a fact, not a verdict on whether the difference is *meaningful* (a rephrased-but-equivalent LLM answer looks identical to a genuine regression at this layer). |
| `Added` / `Removed` | The node exists in only one Timeline — the workflow itself changed between the two runs. |

`Cached` is a byte-equality check, not a claim about *how* the bytes came to match — a node that
coincidentally recomputed the same output looks identical to one served from cache. This is deliberate:
REQ-REPLAY-03 defines the two states by outcome, not by mechanism, so the report says only what's provably
true from the artifacts themselves.

The side-by-side content (`NodeDivergence.OriginalContent`/`NewContent`) is exposed per node; rendering an
actual diff is left to the caller (CLI/UI). Core does not embed a diffing library for a v1 feature no
requirement specifies a rendered format for.
