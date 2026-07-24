# Tutorial — Running `wee` from the terminal

> A hands-on, copy-pasteable walkthrough of the `wee` CLI. Every command and every output block below is
> real — captured from an actual run, not illustrative. Non-normative; the binding behaviour is in
> [spec/cli.md](spec/cli.md) (REQ-CLI-*). If a command here ever disagrees with the code, the code wins.

`wee` runs, replays, and inspects **workflows**: versioned graphs of **Workers** (LLM roles) and **Tools**
(deterministic actions like `terminal`, `git`, `http`). Every run is recorded to an append-only,
hash-chained event log, so any execution can be audited or replayed exactly as it happened.

The fastest way to understand it is to run one. The first workflow below is **tool-only**, so it runs with
**zero configuration and no API key** — start there.

---

## 0. Prerequisites

- **Go 1.22+** (`go version`) — to build the binary from source.
- **An `OPENAI_API_KEY`** — *only* for the LLM-Worker part in [§6](#6-running-an-llm-worker-needs-a-provider-key).
  Everything before that runs offline, for free, and deterministically.

Install a release archive when one is available, or build it from the repo:

---

## 1. Build the binary

From the repo root:

```sh
go build -o wee ./cli
```

That produces a single static binary named `wee` in the current directory. Put it on your `PATH` (e.g.
`sudo mv wee /usr/local/bin/`) or call it as `./wee`. This tutorial writes `wee`.

Confirm it runs:

```sh
wee --help
```

```
wee runs, replays, and inspects workflow executions.

A workflow is a versioned graph of Workers (LLM roles) and Tools (deterministic
actions). Every run is recorded to an append-only, hash-chained event log, so an
execution can be audited or replayed exactly as it happened.

Usage:
  wee [command]

Available Commands:
  backup      Back up or restore the workspace state directory
  cache       Inspect and manage the node cache
  cli         Run the zero-config CLI experience
  completion  Generate the autocompletion script for the specified shell
  export      Export a workflow and its Workers as a portable bundle
  help        Help about any command
  init        Scaffold a workspace with a minimal, runnable example workflow
  inspect     Inspect a recorded execution's nodes, costs, and artifacts
  list        List workflows in this directory and recorded executions
  replay      Audit a recorded execution, or re-execute it and report divergence
  run         Run (or resume) a workflow, streaming events live
  serve       Serve the local control plane (event stream + run controls) for the UI
  validate    Validate a workflow definition against the schema and graph rules

Flags:
  -h, --help   help for wee

Use "wee [command] --help" for more information about a command.
```

---

## 2. Your first run — zero config, no API key

The shortest path is the polished CLI smoke run:

```sh
wee cli
```

It creates a temporary tool-only workflow, records the execution, prints the artifact, and cleans up. Use
`wee cli --keep` if you want to inspect/replay the generated workspace afterwards.

For the manual version, here is the same shape laid bare.

A workflow is a YAML (or JSON) file. This one has a single **tool-backed** node that runs `echo` through
the sandboxed `terminal` tool — no model, no key, fully deterministic.

Make a scratch workspace and drop two files in it:

```sh
mkdir -p ~/wee-demo && cd ~/wee-demo
```

`check.yaml` — the workflow:

```yaml
id: build-check
version: 1.0.0
nodes:
  - id: check
    tool:
      toolName: terminal
      input:
        command: echo
        args: ["build verde"]
edges: []
budget:
  maxCostUsd: 0        # 0 in any dimension means "unlimited" — fine here, there's no model to spend
  maxTokens: 0
  maxDurationMs: 30000
  maxRetriesPerNode: 1
```

`wee.yaml` — the workspace config that tells the `terminal` tool which commands it is allowed to run
(deny-first: nothing runs unless it's on the allowlist — PRIN-10):

```yaml
terminal:
  allow: ["echo"]
  timeoutMs: 5000
```

### Validate it first

`validate` checks the file two ways — against the JSON Schema (shape) and against the graph rules (ids
unique, edges resolve, exactly one of `worker`/`tool` per node):

```sh
wee validate check.yaml
```

```
check.yaml: valid (build-check@1.0.0, 1 node(s))
```

### Run it

```sh
wee run check.yaml
```

```
▶ build-check@1.0.0
  · check
  ⚙ check  tool call
  ✓ check  $0.0000  0 tok  (running $0.0000)

succeeded — 1 node(s), $0.0000, 0 tokens
```

That's a real execution: the engine dispatched the `check` node, called the `terminal` tool, captured its
output as a content-addressed **artifact**, and wrote every step to the event log under `.workflow/`. Cost
is `$0.0000` because a tool call is not a model call.

### See the raw event stream

The human view above is a rendering of an event stream. Ask for the stream itself with `--json` — one JSON
event per line, exactly what the UI consumes live over WebSocket via `wee serve` ([§8](#8-watch-a-run-live-in-the-browser)):

```sh
wee run check.yaml --json
```

```json
{"type":"ExecutionStarted","timestamp":"2026-07-18T12:38:43.933618-03:00","executionId":"build-check-20260718T153843-928f61","prevHash":"4ac6bf...","payload":{"version":"1.0.0","workflow":"build-check"}}
{"type":"WorkerStarted","timestamp":"...","executionId":"...","nodeId":"check","prevHash":"fe1546..."}
{"type":"ToolCalled","timestamp":"...","nodeId":"check","prevHash":"f85ef2...","payload":{"input":{"args":["build verde"],"command":"echo"},"tool":"terminal","version":"1.0.0"}}
{"type":"ToolResult","timestamp":"...","nodeId":"check","payload":{"durationMs":3,"output":{"exitCode":0,"passed":true,"stdout":"build verde\n",...},"tool":"terminal"}}
{"type":"ArtifactCreated","timestamp":"...","nodeId":"check","payload":{"hash":"0b7a1f...","type":"test-result"}}
{"type":"WorkerFinished","timestamp":"...","nodeId":"check","payload":{"costUsd":0,"tokens":0}}
```

Note `prevHash` on every line: each event carries the hash of the one before it, forming a tamper-evident
chain (ADR 0007). That is what makes an execution auditable — you can prove nothing was inserted or altered
after the fact.

---

## 3. Inspect what happened

Every run leaves a record. List the workflows in this directory and the executions recorded so far:

```sh
wee list
```

```
workflows:
  check.yaml  (build-check@1.0.0)
executions:
  build-check-20260718T153834-74e234
```

Copy an execution id and inspect it — this reconstructs the run **from disk, at zero cost** (no re-execution,
no model call):

```sh
wee inspect build-check-20260718T153834-74e234
```

```
execution build-check-20260718T153834-74e234  (build-check@1.0.0)
  check            succeeded   $0.0000       0 tok  90b05fecbb2e  [tool:terminal]
total: $0.0000, 0 tokens
```

Drill into one node to see its detail and the actual artifact content it produced:

```sh
wee inspect build-check-20260718T153834-74e234 --node check
```

```
node check  (build-check-20260718T153834-74e234)
  state:    succeeded
  cost:     $0.0000
  tokens:   0
  type:     test-result
  hash:     90b05fecbb2e06476797afbb001487dbbcd0c82dd556e024455d96c7f70b8eef
  duration: 4.683ms
  artifact:
{"command":"echo","exitCode":0,"passed":true,"stdout":"build verde\n","stderr":"","durationMs":3}
```

The `hash` is the artifact's content address: identical output anywhere in the system is stored exactly once.

---

## 4. Replay — audit vs. re-execute

`replay` has two distinct modes and never conflates them:

**Audit** (default) — reconstruct the past run from the log, like `inspect`, without running anything:

```sh
wee replay build-check-20260718T153834-74e234
```

```
execution build-check-20260718T153834-74e234  (build-check@1.0.0)
  check            succeeded   $0.0000       0 tok  90b05fecbb2e  [tool:terminal]
total: $0.0000, 0 tokens
```

**Re-execute** — run the frozen definition again through the same scheduler and report what diverged
(cached / re-executed / added / removed nodes). This *does* run work:

```sh
wee replay build-check-20260718T153834-74e234 --execute --workflow check.yaml
```

Audit is free and offline; re-execute is the one that can cost money if the graph has Worker nodes.

---

## 5. The cache

`wee` never pays for the same work twice: a Worker node whose inputs, model params, and Contract version
are unchanged is served from a content-addressed cache instead of re-calling the model.

```sh
wee cache ls
```

```
cache is empty
```

It's empty here on purpose: **tool-backed nodes are never cached** (ADR 0008). A tool is an opaque action
(`git diff` today ≠ `git diff` tomorrow), so caching it would be unsafe. Caching applies to **Worker (LLM)
nodes**, where it turns a repeated run into $0.00 — you'll see entries appear once you run the LLM workflow
in §6 twice.

Control caching per run with `--cache`:

```sh
wee run check.yaml --cache off        # ignore and don't write the cache
wee run check.yaml --cache readonly   # read from cache, but don't write new entries
wee run check.yaml --cache on         # default
```

Clear it (artifacts in the store are kept; only the reuse index is dropped):

```sh
wee cache clear
```

---

## 6. Running an LLM Worker (needs a provider key)

Now the model path. `wee init` scaffolds a minimal, runnable example — one Worker, no tools, no inputs:

```sh
mkdir -p ~/wee-llm && cd ~/wee-llm
wee init
```

```
created .workflow/
created examples/hello.yaml
created examples/greeter.worker.yaml

Next: set OPENAI_API_KEY, then run
  wee run examples/hello.yaml
```

`examples/hello.yaml` references the Worker `greeter@1.0.0` (defined in the sibling
`examples/greeter.worker.yaml` — `wee run` auto-loads `*.worker.yaml` files next to the workflow). The
Worker has a **tight output contract**: it must return `{greeting: string ≤ 200 chars}` and nothing else,
so the model can't wander (PRIN-08, anti-slop).

Set your key and run it:

```sh
export OPENAI_API_KEY=sk-...
wee run examples/hello.yaml
```

You'll see the `greet` node go from queued → running → succeeded, with real token counts and a running cost
this time. Then inspect the greeting it produced:

```sh
wee inspect <execution-id> --node greet
```

Run it a second time and the node is served from cache — `$0.0000`, instantly — and `wee cache ls` now shows
the entry.

> **Secrets are references, never values** (NFR-SEC-01). The key is read from the environment at call time;
> it is never written into a definition, an artifact, the event log, or an export. Workflows that need a
> token (e.g. the `github-pr-review` example) reference it as `${env:GITHUB_AUTH_HEADER:-}`, and the resolved
> value is redacted before anything is persisted.

Budgets are enforced *before* the next call, not after the invoice. Override a workflow's cost ceiling for a
run:

```sh
wee run examples/hello.yaml --budget 0.01
```

If a run would exceed the budget, the engine halts and `wee` exits `2` (see [§8](#8-exit-codes)).

---

## 7. Export a portable bundle

`export` bundles a workflow and every Worker it references into a single tar of canonical JSON —
reproducible and hash-identical on round-trip, with no secret values inside (they were only ever
references):

```sh
wee export check.yaml -o build-check.tar
```

```
exported build-check@1.0.0 → build-check.tar (2048 bytes)
```

```sh
tar -tf build-check.tar
```

```
workflow.json
```

---

## 8. Watch a run live in the browser

`wee serve` exposes the workspace over HTTP so the visual UI (`ui/`) can watch an execution happen live —
parallel lanes, cache hits, and a running cost ticker, pushed over WebSocket as they happen (no polling —
see [ADR 0010](adr/0010-websocket-transport.md)).

Start it in the same directory as your workflow:

```sh
wee serve --dir . --workspace .workflow
```

```
wee serve listening on http://127.0.0.1:7676
  workspace: .workflow   workflows under: .
  stream:    GET http://127.0.0.1:7676/api/executions/{id}/events
```

In another terminal, start the UI (see [ui/README.md](../ui/README.md)):

```sh
pnpm --dir ../ui dev
```

Open the UI, import `check.yaml`, and click **Run** in the toolbar — it posts to `wee serve`'s
`POST /api/run` with the imported file's name, then watches the returned execution live: the node's border
and badge move through `running` → `succeeded`/`cached`/`failed`, edges animate while data is flowing into a
running node, and the Timeline panel below draws a Gantt bar per node (cache hits colored distinctly from a
fresh success) alongside a live `$cost` / token ticker. The Artifacts and Logs tabs fill in as their events
arrive — never only after the run finishes.

You can start a run the same way the UI does — `POST /api/run` is plain HTTP/JSON, so `curl` works directly:

```sh
curl -s -X POST http://127.0.0.1:7676/api/run -d '{"workflow":"check.yaml"}'
# {"executionId":"build-check-20260718T155700-596265"}
```

The events endpoint itself is a WebSocket upgrade, which plain `curl` doesn't speak — inspect it with your
browser's DevTools **Network** tab (filter to **WS**) while the UI is watching a run, or with a small
WebSocket CLI client (e.g. `websocat`) if you want a terminal-only look:

```sh
websocat "ws://127.0.0.1:7676/api/executions/build-check-20260718T155700-596265/events"
```

Either way you'll see one JSON text frame per `domain.Event` — byte-identical to a line of `wee run --json`
— with the connection closing cleanly (`StatusNormalClosure`) once `ExecutionFinished` arrives.

> The **Run** button posts the imported file's *basename* (browsers never expose a full path) — it only
> resolves if `wee serve --dir` points at the same folder the file was imported from. A mismatch surfaces as
> an error in the toolbar (the server's 400), not a silent failure.

---

## 9. Command reference

| Command | What it does |
|---|---|
| `wee init` | Scaffold `.workflow/` and a runnable `examples/hello.yaml` + Worker. |
| `wee cli` | Run the zero-config, no-key CLI experience with polished terminal output. |
| `wee validate <file>` | Check a workflow against the schema and graph rules. |
| `wee run <file>` | Run (or `--resume <id>`) a workflow, streaming events live. |
| `wee run <file> --json` | Same, but emit line-delimited event JSON (what `wee serve` also streams). |
| `wee list` | List workflows in the directory and recorded executions. |
| `wee inspect <id>` | Reconstruct a recorded run from disk (add `--node <id>` for detail + artifact). |
| `wee replay <id>` | Audit a recorded run; `--execute` runs it again and reports divergence. |
| `wee cache ls` / `inspect <key>` / `clear` | Inspect and manage the node cache. |
| `wee backup create <archive.tar.gz>` / `restore <archive.tar.gz>` | Back up or restore the workspace state directory. |
| `wee export <file> [-o out.tar]` | Bundle a workflow + its Workers into a portable tar. |
| `wee serve [--addr host:port] [--dir .] [--workspace .workflow] [--templates dir] [--ui-dir ui/dist]` | Serve the run API, live WebSocket stream, and optionally the built UI. |

Common flags on `run`: `--cache on\|off\|readonly`, `--concurrency N` (0 = engine default),
`--resume <id>`, `--budget <usd>` (override the workflow's max cost), `--workspace <dir>` (state directory,
default `.workflow`), `--allow-mutations-without-approval` (explicitly bypass approval checkpoints).

## 10. Exit codes

`wee` maps outcomes to process exit codes (REQ-CLI-04) so it composes in scripts and CI:

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | A node failed. |
| `2` | Budget exceeded — the run was halted before overspending. |
| `3` | Validation error (bad schema or graph). |
| `130` | Cancelled (Ctrl-C / SIGINT). |

```sh
wee run check.yaml && echo "green, safe to proceed" || echo "failed with code $?"
```

---

## Where state lives

Everything a run produces is under the workspace state directory (default `.workflow/`, override with
`--workspace`):

```
.workflow/
  executions/<id>/   # the hash-chained event log + the frozen definition snapshot
  artifacts/<hash>   # content-addressed artifact store (each output stored once)
  cache/             # the node-reuse index
```

The event log is the **single source of truth** (PRIN-02): the live `run` output, `--json`, `inspect`,
`replay`, and the UI all read from it — there is never a second, divergent record. Delete `.workflow/` and
you delete the history; the workflow definitions (`*.yaml`) are untouched.

---

## What's next

- Build workflows **visually**: the React UI (`ui/`) reads and writes the same canonical YAML — see
  [ui/README.md](../ui/README.md) (`pnpm --dir ui dev`).
- **Watch a run live in the browser** — `wee serve` streaming to the UI canvas and a Gantt timeline — see
  [§8](#8-watch-a-run-live-in-the-browser) above.
- Build workflows **in Go**: the authoring SDK (`sdk/`) — see [concepts/sdk.md](concepts/sdk.md).
- The deeper model: start at [AGENTS.md](../AGENTS.md), then [spec/cli.md](spec/cli.md) and
  [spec/ui.md](spec/ui.md) for the binding requirements this tutorial demonstrates.
