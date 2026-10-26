# Case study — the flagship PR-review-and-fix run

> This is the narrative version of the technical record kept in
> [examples/pr-review-autofix/README.md](../examples/pr-review-autofix/README.md). That file is the source
> of truth (exact run IDs, token counts, commit hashes); this page tells the same story for a reader who
> wants to understand what happened and why it's evidence of anything, without reading a spec.

## The graph

`pr-review-autofix` is 21 nodes: three independent reviewers (style, adversarial, security) run in
parallel, each scoped to see only the diff, not the repo. Their findings feed a Fixer, whose own proposed
correction is judged by a separate, cheaper verifier before anything touches a file. Only after that
approval does a real write/test/stage/commit tail run — and only after that does a second tail push a real
branch and open a real pull request, one authenticated POST per GitHub Git Data API step
(blob → tree → commit → ref → PR).

Every mutating step — the write, the test, the stage, the commit, each of the five GitHub POSTs — pauses at
its own approval checkpoint. Nothing here runs unattended.

![The pr-review-autofix workflow mid-run in the wee UI](assets/pr-review-autofix-run.png)

## Picking a real target, and what "safe" actually looked like

The proof requirement was strict: run this against a real public repository, with a real model, produce a
real corrective commit — and never let a wrong output reach a file. Three targets were tried before one
succeeded, and the two that didn't are the more interesting result:

- **[spf13/cobra PR #2447](https://github.com/spf13/cobra/pull/2447)** — the target file was too large for
  the Fixer's full-file rewrite to return inside the provider's timeout. All three attempts failed
  transiently. The run stopped before any approval checkpoint; nothing was written. Rejected as a target
  rather than loosening the timeout.
- **[google/uuid PR #171](https://github.com/google/uuid/pull/171)** — the reviewer correctly found the
  regression, but the only safe correction was identical to the base file, so there was nothing to commit.
  Rejected rather than manufacturing an unrelated change to force a result.
- **[go-chi/chi PR #1142](https://github.com/go-chi/chi/pull/1142)** — first attempt: the extraction
  Worker that builds the original file's URL used the PR's *head* commit instead of its *base* commit,
  against its own constraint. The Fixer was therefore handed the already-patched file and returned it
  byte-for-byte unchanged. The verifier rejected it *before* the approval checkpoint — the right outcome —
  but its stated reason ("a diff fragment, not a full file") was wrong. An earlier version of this page
  said the rejection was for a dropped copyright header; the recorded execution does not support that, and
  the file has no such header. Later attempts drove the hardening that preceded the accepted run
  (ordered-fallback review, forcing raw-JSON full-file output, a stronger model for the two full-file
  reasoning steps), and the URL field now has to pin a full commit sha.

That sequence is the actual claim being made, and its limit: a producer's unreliability — even a wrong
identifier plus a judge with the wrong explanation — degraded to "nothing happens," never to "something
wrong got applied." The guarantee comes from layering (a schema-checked boolean gating a deterministic edge,
then a human approval of the exact input), not from any one model being right; nothing in it verifies that a
model-transcribed identifier is the *correct* one.

## The successful run

Execution `pr-review-autofix-20260723T172016-1d0337` reviewed go-chi/chi PR #1142 with a real OpenAI key,
cache disabled, and a $0.20 budget ceiling. Every model and deterministic node succeeded. The run paused
four times for explicit approval — before writing `middleware/compress.go`, before running `go test ./...`
(26.667s, exit 0), before staging, and before the local commit — landing a real fix for an
Accept-Encoding precedence bug (explicit `q=0` was losing to a later wildcard match) at a cost of
**$0.069 and 35,142 tokens**, entirely inside a disposable clone.

| Nodes | Provider / model | Tokens | Cost |
| --- | --- | ---: | ---: |
| reviewers + `locate-file` | OpenAI / `gpt-4o-mini` | 14,233 | $0.0024 |
| `fixer` | OpenAI / `gpt-4.1` | 10,881 | $0.0463 |
| `verify-fix` | OpenAI / `gpt-4.1` | 10,028 | $0.0203 |
| **Total** | | **35,142** | **$0.0690** |

No upstream write was authorized against go-chi/chi itself — the owner's call was to leave the finished
execution recorded rather than open a competing PR against someone else's live repository. The same graph,
run separately against a disposable sandbox repo, did carry the tail through to a real, mergeable pull
request:

![A real GitHub pull request opened by the workflow](assets/pr-review-autofix-pr-opened.png)

## What this is evidence of

Not that a model-generated fix is always correct — the two rejected targets above are the counter-evidence
kept on purpose. What it demonstrates is the thesis in [VISION.md](VISION.md): **engineer the context,
delegate to the model only what actually fits in it.** The runtime decided when to retry, when to stop,
and when a human had to look before anything mutated; the model's only job at each step was the one
narrow thing it's actually good at — reviewing a diff, proposing a full-file correction, composing a PR
title. Every dollar and token spent is attributed and auditable from the event log, not estimated after
the fact.
