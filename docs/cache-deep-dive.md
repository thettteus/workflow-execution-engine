# Cache Deep Dive

Non-normative. The testable rules are [spec/cache.md](spec/cache.md) (`REQ-CACHE-*`). Implementation:
`core/cache/*.go`. CLI surface: [cli-reference.md](cli-reference.md#wee-cache).

The node cache is why the second run of anything unchanged is free: a node whose complete set of inputs
hasn't changed since it last ran returns its recorded artifact instead of calling the model (or invoking
the tool) again. Changing one node re-executes only that node and its downstream cone — everything upstream
and everything on an unaffected branch is served from cache at $0.

## What the key is made of — and, just as important, what it isn't

```go
type Inputs struct {
    WorkerID            string
    WorkerVersion        string
    ContractHash        string
    InputArtifactHashes []string
    Model               domain.ModelConfig
    ToolVersions        []string
    ContextPolicy       domain.ContextPolicy
    WorkflowInputs      map[string]string
    DefinitionHash      string
}
```

`DefinitionHash` is the canonical hash of the Worker definition's model-visible fields (everything except
`description`). `WorkerVersion` was meant to stand in for them, but version immutability is enforced per
registry, which means per process. A `*.worker.yaml` edited in place without a version bump reached the
next run as different content under the same `id@version`, and was served the old output from cache. Adding
the field invalidated existing cache entries once.

The key is the SHA-256 of the canonical JSON (ADR 0004) of exactly these fields (REQ-CACHE-01) — nothing
else, nothing less. `InputArtifactHashes` and `ToolVersions` are sorted before hashing, so edge-declaration
order or tool-allowlist order never perturbs the key; only their *content* does. Notably absent: a
timestamp, an execution id, a random seed — none of those are inputs to what the node produces, so none of
them belong in what invalidates it.

**Invalidation is total, not fuzzy.** Any change to any field — a Worker bumped a patch version, a Contract
gained one more `required` field, an upstream artifact's content changed by one byte, a Worker's model
config switched temperature — yields a wholly different key. There is no partial credit, no "the inputs are
basically the same" heuristic. This is a deliberate simplicity choice for Phase 1: a byte-for-byte
invalidation rule is auditable and predictable; a fuzzy one would need its own explanation of when it
does and doesn't fire.

## Storage: a thin index over the artifact store

```
<workspace>/cache/index.json     — key → Entry (this package)
<workspace>/artifacts/           — content-addressed bytes (core/store, keyed by hash)
```

An `Entry` is a *reference*, never a copy: `ArtifactHash` points into the shared store the same way every
other Artifact reference does. Two different cache keys whose nodes happen to produce byte-identical output
point at the same stored bytes — deduplication happens at the store layer, for free, regardless of the
cache. `Entry` also carries the original `CostUSD`/`Tokens`, so a hit can report what it *saved*
(REQ-METRIC-03, `CacheHit.payload.savedCostUsd`) without recomputing anything.

The index is written atomically (temp file + rename) — a crash mid-write never leaves a half-written index
for the next run to choke on.

## A hit still gets real events

A cache hit is not a silent skip. The engine reconstructs the node's event sequence fresh for *this*
execution's log — `CacheHit` in place of `CacheMiss`, still followed by `ArtifactCreated`/`WorkerFinished`
— rather than replaying the original run's stored events verbatim. Copying a past event stream would carry
a stale execution id and break the new log's hash chain (ADR 0007); re-deriving it keeps every execution's
log internally consistent while still reporting truthfully that the artifact itself is reused, not
recomputed.

## Modes — `wee run --cache <mode>`

| Mode | Behavior |
|---|---|
| `on` (default) | read hits, write new entries |
| `off` | ignore the cache entirely — every node re-runs, nothing is read or written |
| `readonly` | read hits normally, but never write a new entry — useful for a dry run you don't want to pollute the index |

## Inspecting and clearing — `wee cache ls` / `inspect` / `clear`

```sh
wee cache ls                 # every entry: key, artifact, cost saved
wee cache inspect <key>       # one entry's recorded result
wee cache clear               # empty the index — artifacts in the store are untouched
```

"A cache you can't inspect is a cache you can't trust" — `wee cache ls`/`inspect` exist so a surprising
cache hit (or miss) is a one-command lookup, not a mystery. `wee cache clear` only empties the index;
because artifacts are content-addressed and shared, clearing the cache never deletes anything the audit
trail or another execution still references — it only forces every node to prove its output again.

## Related

- [concepts/artifact.md](concepts/artifact.md) — the content-addressed store the cache references, never copies
- [concepts/budget.md](concepts/budget.md) — how a cache hit becomes budget headroom, priced at $0 and tracked separately as saved spend
- [replay-honesty.md](replay-honesty.md) — how re-execution (`wee replay --execute`) relies on this same cache
