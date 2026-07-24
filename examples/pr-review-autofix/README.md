# pr-review-autofix

The flagship demo ([VISION.md](../../docs/VISION.md) "Pull Request Review & Auto-Fix") — every
differentiator the project claims, in one graph:

```text
fetch-pr
fetch-diff
  +-- reviewer-a        (style & correctness)
  +-- reviewer-b        (adversarial — assumes the diff is wrong)
  +-- security-reviewer (vulnerabilities)      <- all three in parallel
  +-- locate-file       (extracts the path and raw base-file URL)
        v
      read-original (tool: http read — the real base file locate-file named)
        v
      fixer        (reads the diff + all three reviews + the real original file)
        v
      verify-fix   (REQ-CONTRACT-05 — a separate, cheap judge; see below)
        v (conditional: approved == true)
      push-plan    (composes branch name / Git Data API URLs / PR title+body)
        v
      checkout-branch (tool: git branch — a local feature branch, not the base)
        v
      apply-fix    (tool: filesystem, writes fixer's corrected file)
        v
      test          (tool: terminal)
        v
      stage         (tool: git add)
        v
      commit        (tool: git commit)
        v (ordering — nothing below touches GitHub before the local commit)
      get-base-commit (tool: http GET  — read-only, the base commit's tree sha)
        v
      create-blob     (tool: http POST — the corrected file's new blob)
        v
      create-tree      (tool: http POST — base tree + the one changed blob)
        v
      create-commit    (tool: http POST — the new remote commit object)
        v
      create-branch    (tool: http POST — the remote ref, pointing at it)
        v
      open-pr          (tool: http POST — the pull request itself)
```

Three independent reviewers, each scoped to see only the diff (`contextPolicy: {mode: artifacts,
params: {artifacts: [fetch-diff]}}` — see [concepts/context-policy.md](../../docs/concepts/context-policy.md)
for why `artifacts` mode, not `diff-only`, is what actually works here); a `locate-file` Worker + a
`read-original` HTTP read that hand the Fixer the real, complete original file from the PR base commit —
not just the diff — to base its rewrite on (see "Disclosed, current limitation" below for the failure this
closes); a Fixer
whose own output is judged by a separate `verify-fix` Worker before anything touches a file; real
tool-backed apply/test/stage/commit steps; a real GitHub push/PR tail; node-cache reuse across re-runs.

## The GitHub push/PR tail

`push-plan` through `open-pr` land the accepted fix on GitHub as a real branch and a real pull request —
without ever asking the `git` tool to push. `core/tool/git` deliberately has no push op (its own header
comment: "the engine never reaches a remote") — landing the change instead goes entirely through the
generic `http` tool against GitHub's Git Data API, one POST per step (create blob → create tree → create
commit → create ref → open PR), exactly the "workflow-defined integration" pattern
[ROADMAP.md](../../docs/ROADMAP.md)/[EXECUTION-PHASE2.md](../../docs/EXECUTION-PHASE2.md) describe for
M2.5's Change Auto-Fix acceptance.

Building this surfaced two real gaps in the `http` tool, both closed alongside this graph
([docs/spec/tools.md](../../docs/spec/tools.md) REQ-TOOL-06/07):

- **REQ-TOOL-06 (request side):** the tool's body used to be one opaque string — no way to mix a static
  JSON key (`"encoding": "utf-8"`) with a dynamic one (the file's own content) without a Worker
  hand-assembling and escaping an entire JSON document itself. `bodyJson` resolves each field individually
  through the same whole-string placeholder mechanism every other tool input already uses
  (`core/engine/tool_input.go`, ADR 0008) — no new templating engine, no string concatenation.
- **REQ-TOOL-07 (response side):** chaining GitHub's Git Data API means each step's input is the
  *previous* step's server-assigned sha, known only after that call runs. Routing an exact 40-character hex
  string through a Worker risks exactly the kind of transcription error that would silently corrupt the
  chain. The response now carries a parsed `bodyJson` field alongside the raw `body` string whenever the
  body is itself a JSON object/array, so `"${create-blob.bodyJson.sha}"` resolves deterministically through
  the engine's existing `lookupPath` walker — never retyped by a model.

GitHub's Git Data API (not the simpler Contents API) is what makes this possible without base64 at all: its
blob-creation endpoint accepts `"encoding": "utf-8"` directly, and no base64 primitive exists anywhere in
`core` — the Contents API, by contrast, requires base64 unconditionally.

`push-plan` is the one Worker in this tail, and it does only what an LLM is actually suited for here:
composing precomposed strings (URLs, a branch name, a PR title/body) the same way `locate-file` already
composes a raw.githubusercontent.com URL — never shuttling an exact server-assigned id, which is what
REQ-TOOL-07 is for instead.

Every POST in this tail — `create-blob`, `create-tree`, `create-commit`, `create-branch`, `open-pr` — is a
real, authenticated write to a real repository, and every one of them pauses at its own approval checkpoint
(REQ-TOOL-05) with the request body shown before it sends, exactly like `apply-fix`/`test`/`stage`/`commit`
already do. Nothing here is unattended by default: a full run now pauses **nine** times (branch checkout,
write, test, stage, local commit, blob, tree, commit, ref+PR — `create-branch` and `open-pr` are the eighth
and ninth), not four.

## Five-minute public demo path (M2.12)

This is the repeatable product walkthrough for the packaged template. Use a
**disposable clone** as `WEE_WORKSPACE_ROOT`; the workflow can write, test,
stage, and commit after explicit approvals.

1. Start `wee serve` with the built UI and `--templates examples/templates`,
   with `WEE_WORKSPACE_ROOT` pointing at the disposable target checkout.
2. Open **Templates** and choose `pr-review-autofix`. Before import, confirm the
   card says **write-capable**, requires the `openai` model connection, declares
   the required `prUrl` input, and lists filesystem/Git/HTTP/terminal tools.
3. In **Settings → Model providers**, add or update the `openai` connection and
   set its key. In the Run dialog, supply a public GitHub PR API URL such as
   `https://api.github.com/repos/OWNER/REPO/pulls/N`.
4. On the imported canvas, point out the published split: **7 model** nodes and
   **14 deterministic** nodes. Select one of each. The Inspector shows the
   Worker's name/version/description and resolved `provider / model-id`, or an
   explicit **no model** plus the recorded Tool name/version.
5. Run. Review/fix proposal generation may proceed without workspace mutation.
   If `verify-fix` approves a proposed correction, execution pauses before
   `checkout-branch`; inspect the formatted change, affected path, command/API
   preview, and remaining budget. Nothing mutating has run at this point.
6. Reject to demonstrate the safe stop, or approve only in the disposable
   clone. Later mutating steps (`checkout-branch`, `test`, `stage`, `commit`,
   and the `create-blob`/`create-tree`/`create-commit`/`create-branch`/
   `open-pr` GitHub push/PR tail) remain independently checkpointed; approval
   of one operation never approves another. The push/PR tail additionally
   requires `GITHUB_AUTH_HEADER` to carry write access to the target repo —
   without it, `create-blob` fails loud with GitHub's own 401/403 rather than
   silently skipping.

What this demo proves now: the real gallery bundle imports through the canonical
workflow path; model/deterministic identity is visible and tied to the frozen
execution snapshot; and write-capable work pauses at persistent, explicit
approval checkpoints. It does **not** prove that a model-generated correction
is correct, that every PR is single-file/supported, or that the post-
`read-original` workflow has completed the new public-repository proof. That
real, recorded run is M2.13; the historical M1.15 runs below predate the
`read-original` correction and are disclosed as such.

## Inspiration

The implement → adversarial-review → fix → verify → commit shape here is a deliberate nod to the
[Zig-to-Rust port of Bun](https://bun.com/blog/bun-in-rust) (Jarred Sumner + Claude, July 2026), which ran a
similar Write → Attack → Fix → Commit loop across many parallel Claude Code workflows. There the
orchestration lived in a conversation and was thrown away once the port landed. WEE's bet is that the loop is
the durable part: the same graph, kept as a versioned, budgeted, cached, replayable definition instead of
reconstructed by hand each time.

## Running it

Requires:

- `prUrl` — a declared workflow input ([concepts/workflow.md](../../docs/concepts/workflow.md),
  REQ-INPUT-01): the specific PR diff URL this run reviews. Supply it with
  `wee run workflow.yaml --input prUrl=https://api.github.com/repos/OWNER/REPO/pulls/N`, or pick it in the
  UI's Run dialog after importing the template — either way it's recorded in the run's audit trail.
- Public GitHub PR reads are anonymous by default. If the shared-IP quota is exhausted — or once
  `checkout-branch` onward is allowed to run at all — set `GITHUB_AUTH_HEADER` to `Bearer <token>` in
  Settings; it is sent but never recorded. The push/PR tail (`create-blob` through `open-pr`) needs a token
  with **write** access to the target repo, not just enough to lift the anonymous read quota.
- A `wee.yaml` allowlisting GitHub hosts plus the terminal command this repo's tests/build run with (`go`
  by default here). The workspace root is only used by the local tail (`checkout-branch`/`apply-fix`/`test`/
  `stage`/`commit`); point it at a real disposable git checkout of the **target** repo before allowing those
  nodes to run — never this engine's own repo, and never a path under its own `.workflow/` directory (that
  directory is gitignored; `git add` from inside a gitignored directory fails before any commit is made).
- `OPENAI_API_KEY` (or `ANTHROPIC_API_KEY`, per each Worker's `model.provider`) for the seven LLM Workers.

Per-repo tuning (test command, timeouts, budget) is expected — this bundle's defaults are a starting point,
not a one-size-fits-all config. See "Real-repo validation" below for what tuning three actual repos needed.

## Expected cost

Seven LLM Workers per run: three `gpt-4o-mini` reviewers (parallel, cheap), one `gpt-4o-mini` `locate-file`
(cheap — path + raw URL extraction), one `gpt-4.1` fixer (the expensive call), one `gpt-4.1` verifier, and one
`gpt-4o-mini` `push-plan` (cheap — composes the branch name/API URLs/PR title+body, gated behind
`verify-fix`'s approval so a rejected fix never pays for it). The GitHub push/PR tail itself
(`get-base-commit` through `open-pr`) is all deterministic `http` tool calls — no further model cost. A
typical run costs a few cents and can approach fifteen cents for a larger corrected file — see the real
figures below — inside the workflow's own `maxCostUsd: 0.60` ceiling; a run where `verify-fix` rejects the fix skips
`checkout-branch` onward (including the entire push/PR tail) and costs less still. The actual figure for any
specific run is real accounting, not an estimate — see it via `wee inspect <id>` or the UI's Metrics panel
([concepts/budget.md](../../docs/concepts/budget.md)).

## Flagship public proof (M2.13)

### Initial target and safe abort

The first selected target was [spf13/cobra PR #2447](https://github.com/spf13/cobra/pull/2447), which addresses
[issue #1911](https://github.com/spf13/cobra/issues/1911) by adding `RemoveGroup` to a widely used Go CLI
library. The PR is a suitable low-risk proof target because it changes only `command.go` (17 added lines),
the repository has a fast `go test ./...` validation path, and the run takes place from the PR's recorded
base commit (`adbc8813901bba65827259daa8e22ff94ec1f30e`) in a disposable clone.

The defect under review is public and concrete: an
[inline review comment](https://github.com/spf13/cobra/pull/2447#discussion_r3634944060) notes that removing
a group can leave a child command's `GroupID` pointing at the removed group, after which Cobra's group
validation fails on the next execution. The workflow is expected to review that risk and may propose the
smallest correction to `command.go`; correctness is still decided by the verifier, the real test suite, and
the human approval checkpoints.

Execution `pr-review-autofix-20260723T153207-2a60d1` stopped safely before `verify-fix` or any approval
checkpoint: `command.go` was too large for the full-file Fixer response to arrive within the provider
client's 60-second timeout, and all three bounded attempts ended as transient provider failures. The
disposable clone remained clean and no mutation event was emitted. This target was therefore rejected for
the flagship recording instead of weakening the timeout or bypassing the safety path.

### Second target and verifier rejection

The next target was [go-chi/chi PR #1142](https://github.com/go-chi/chi/pull/1142), against base
commit `8b258c7bb28f97a5f2a856ff7ef962578fec9215`. It addresses the public
[Accept-Encoding issue #1069](https://github.com/go-chi/chi/issues/1069) in the compact
`middleware/compress.go` file. The proposed matcher adds wildcard support but returns on the first wildcard
or exact match it encounters; that makes the answer header-order-dependent and can let `*;q=1` override a
later explicit `gzip;q=0`, even though the explicit exclusion must take precedence. This is a concrete,
bounded HTTP-negotiation defect with a fast `go test ./...` validation path.

Execution `pr-review-autofix-20260723T154012-3b8c09` completed its review and Fixer stages, but
`verify-fix` rejected the proposed file before any approval checkpoint: although the content was file-shaped,
it omitted the original copyright header and therefore diverged outside the targeted edit. The clone again
remained clean. This is the intended fail-closed result and is retained as safety evidence, not used as the
successful flagship run.

### Third candidate and no-op correction

[google/uuid PR #171](https://github.com/google/uuid/pull/171), against base commit
`0e97ed3b537927cb4afea366bc4cc36f6eb37e75`, was also evaluated because an
[inline maintainer review](https://github.com/google/uuid/pull/171#discussion_r1842602724) identifies a
specific regression in one compact file. Runs `pr-review-autofix-20260723T154707-172df6`,
`pr-review-autofix-20260723T155057-6ff608`, and `pr-review-autofix-20260723T155437-e58dee` all stopped
without mutation: the hardened reviewer eventually found the 36+2 bracketed-UUID regression, but the safe
correction was exactly the base file and therefore could not produce the real corrective commit M2.13
requires. The target was rejected rather than manufacturing an unrelated change.

### Final target and successful proof

The final target returned to [go-chi/chi PR #1142](https://github.com/go-chi/chi/pull/1142), whose
explicit-token-versus-wildcard defect yields a material correction from base. Before the final run, the
failed-safe attempts above drove narrowly scoped template hardening: the adversarial review now checks
ordered fallback precedence, the Fixer must return raw JSON and implement rather than repeat a reviewed
hunk, `gpt-4.1` handles the two full-file reasoning steps, and `fixer -> apply-fix` directly wires the
artifact that the filesystem input references.

Execution `pr-review-autofix-20260723T172016-1d0337` ran workflow `pr-review-autofix@1.1.1` with cache off,
a real OpenAI key, and a `$0.20` run budget. Every model and deterministic node succeeded. The run paused
four times and received a distinct approval before:

1. writing only `middleware/compress.go`;
2. running `go test ./...` (`26.667s` for `middleware`, exit `0`);
3. staging only `middleware/compress.go`; and
4. creating the local commit.

| Nodes | Provider / model | Tokens | Cost |
| --- | --- | ---: | ---: |
| reviewers + `locate-file` | OpenAI / `gpt-4o-mini` | 14,233 | $0.00242295 |
| `fixer` | OpenAI / `gpt-4.1` | 10,881 | $0.04632600 |
| `verify-fix` | OpenAI / `gpt-4.1` | 10,028 | $0.02028400 |
| **Total** |  | **35,142** | **$0.06903295** |

The disposable clone started at `8b258c7bb28f97a5f2a856ff7ef962578fec9215` and finished clean at local
commit `40756ed43a6335142ef15bfa428b3cb1416e93ea`
(`fix Accept-Encoding precedence to honor explicit q=0 over wildcard`). No upstream write was authorized:
the requested handoff was to leave the completed execution in the UI for the owner to record, not to push a
branch or open a competing PR against the existing public contribution. The persisted execution provides
the video/still source; a public draft requires a separate owner decision.

## Real-repo validation (M1.15)

Run for real against three public repos of different sizes — [google/uuid](https://github.com/google/uuid)
(small), [spf13/cobra](https://github.com/spf13/cobra) (medium), and
[prometheus/prometheus](https://github.com/prometheus/prometheus) (large) — each against a real, recent
merged PR, in a disposable local clone (nothing pushed anywhere). This is real accounting, not an estimate:
runs cost $0.0050–$0.0110 each, all inside a tightened `--budget 0.20` cap.

**Bugs found and fixed as a direct result** (none of these were caught by schema/graph validation or unit
tests alone — they only surfaced running the graph against a real model and a real repo):

- `stage`/`commit` referenced `${fixer.path}`/`${fixer.commitMessage}` without a direct edge from `fixer` —
  context resolution only ever sees direct parents, so both placeholders failed to resolve. Fixed by adding
  `fixer -> stage` and `fixer -> commit` edges alongside the existing ordering edges. The same missing-edge
  bug existed in [bug-investigation](../bug-investigation/README.md)'s `apply-patch` (fixed there too).
- [pr-review](../pr-review/README.md)'s commit message embedded a placeholder in a larger string
  (`"${review.verdict}: automated review pass"`) — placeholders are whole-string only (REQ-WORKER-06); a
  partial match is never substituted and is committed as literal, unresolved text. Fixed by making the
  message exactly `"${review.verdict}"`.
- Large repos need per-repo tuning to stay within a reasonable time/cost budget: prometheus's `test` node
  was scoped from `go test ./...` (the whole suite) to `go build ./cmd/prometheus/...` (a fast, real
  compile check relevant to the PR under review), with `wee.yaml`'s terminal `timeoutMs` and the workflow's
  own `maxDurationMs` both raised.

**A real safety gap found, and REQ-CONTRACT-05 applied to close it:** the first validation pass (before
`verify-fix` existed) found `fixer` occasionally emits a raw diff fragment (`diff --git a/...`, `+++`,
`@@` hunk headers) as its `content` field instead of the actual corrected file — a shape no `outputSchema`
alone can reject (`content` is just `{type: string}`; a diff fragment is still a syntactically valid
string). Concretely, this once **overwrote prometheus's `AGENTS.md` with diff syntax** instead of the real
corrected file, in a disposable local clone — a genuine, if contained, failure. `verify-fix` (mirroring
[bug-investigation](../bug-investigation/README.md)'s `verify-patch` pattern) was added specifically to
catch this: a separate, cheap Worker judges `fixer`'s own output before `apply-fix` ever runs.

**Root cause found and fixed:** with `gpt-4o` at `temperature: 0`, `fixer` produced a diff-fragment
`content` far more often than a real full-file rewrite when it's shown the diff as its own context. The
actual cause: `fixer` never had the real file to begin with — only the diff, which for anything but a
trivially short file shows nowhere near every unchanged line. Asked for "the complete corrected file" with
no source for most of that file's own content, the model had little choice but to echo back something
diff-shaped. Across repeated runs against all three validation repos, `verify-fix` correctly rejected this
every time (`"content is a diff fragment, not a full file."`), so **no file was ever corrupted** — but it
also meant the auto-fix step frequently never reached an applied fix. Fixed by adding `locate-file` (a
cheap Worker that extracts the modified path from the diff and builds the raw GitHub URL for the PR base
file) and `read-original` (an `http` read of that real file) upstream of `fixer`, so `fixer` now edits the
actual file content instead of reconstructing it from a partial diff — and `verify-fix` now also sees
`read-original`, so it can reject a `content` whose unrelated lines diverge from the real file, not just
pattern-match diff syntax. **Re-validated against a real repo in M2.13:** execution
`pr-review-autofix-20260723T172016-1d0337` completed the guarded write/test/stage/commit path against
go-chi/chi after the separate verifier approved the full corrected file. The preceding rejected runs remain
evidence of REQ-CONTRACT-05's intended failure mode: a producer's unreliability degrades to "nothing
happens," never to "something wrong got applied."
