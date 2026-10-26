# Spec — Node Cache

**Prefix:** `REQ-CACHE` · **Status:** STABLE (delivery M1.6; remote in M2.2) · **Principles:** PRIN-05
(core), PRIN-01 · **Implementation:** `core/cache/` (M1.6)

The other strongest differentiator, inspired by Turborepo/Nx: never pay for the same work twice. A node
whose inputs haven't changed returns its cached artifact instead of calling the model — re-running a
workflow after changing one node re-executes only downstream of the change.

### REQ-CACHE-01 — Deterministic cache key
The engine shall derive a node's cache key from the canonical hashes of: Worker version, Contract version,
the Worker definition's model-visible content, resolved workflow inputs, resolved input artifacts, model +
parameters, and tool versions — nothing else, nothing less.
- **Rationale:** PRIN-01 — the key *is* the reproducibility statement; content addressing (ADR 0004) makes
  it byte-stable.
- **Implementation note:** the definition-content hash (`cache.Inputs.DefinitionHash`, added 2026-10-25)
  covers objective and constraints. The version alone stood in for them only while versions are immutable,
  and immutability is enforced per registry (per process), so an in-place edit without a bump used to be
  served stale output.
- **Delivered by:** M1.6. **Verified by:** `cache.TestKeyDeterministicAndOrderInsensitive`,
  `TestKeyChangesOnAnyFieldChange`, `engine.TestChangingWorkflowInputInvalidatesWorkerCache`,
  `engine.TestInPlaceWorkerEditInvalidatesCache`.

### REQ-CACHE-02 — Hit returns the recorded artifact
When a node's cache key matches a prior execution, the engine shall return the cached artifact
byte-identically, emit `CacheHit` (with the key), skip the model call entirely, and record zero
cost for the node; on a miss it shall emit `CacheMiss`.
- **Delivered by:** M1.6. **Verified by:** `engine.TestSecondRunIsAllCacheHitsAtZeroCost` (unchanged re-run →
  100% hits, \$0, zero model calls).

### REQ-CACHE-03 — Precise invalidation
If one node's definition or inputs change, then the engine shall re-execute only that node and its
downstream cone; unchanged siblings stay cached.
- **Rationale:** PRIN-05 — iteration cost is proportional to the change, not the workflow.
- **Delivered by:** M1.6. **Verified by:** `engine.TestChangingOneNodeReExecutesOnlyItsCone` (bump one node →
  only it + downstream re-run; upstream/sibling stay cached).

### REQ-CACHE-04 — Cache modes and inspection
The engine shall support `cache=on|off|readonly` per run, and the CLI shall expose `wee cache
ls|inspect <key>|clear`.
- **Rationale:** PRIN-02 — a cache you can't inspect is a cache you can't trust.
- **Delivered by:** M1.6 (core: modes + `List`/`Inspect`/`Clear`), M1.9 (CLI). **Verified by:**
  `cache.TestListAndClear`; per-run modes exercised by the engine cache tests. CLI surface _pending_ (M1.9).

Saved spend from cache hits feeds savings accounting — see REQ-METRIC-03.
