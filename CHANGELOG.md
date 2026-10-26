# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project will follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) from its first tagged release.

## [Unreleased]

Nothing has been released yet — there is no tag, no published binary, and no v1-stable API. Development
history before this point is tracked at milestone granularity in [docs/ROADMAP.md](docs/ROADMAP.md) and the
execution plans ([Phase 1](docs/EXECUTION.md), [Phase 2](docs/EXECUTION-PHASE2.md)), which record what each
milestone delivered and how it was verified. Versioned entries here begin at the first tag.

### Added

- CI enforcement of the PRIN-04 forbidden-vocabulary rule, via `scripts/check-vocabulary.sh` and its own
  workflow. The rule was previously documented as enforced but was only ever a manual grep.
- `make vocab`, to run that gate locally.
- `CONTRIBUTING.md`, `SECURITY.md`, and this changelog.

### Security

- `wee serve` answered every origin with `Access-Control-Allow-Origin: *` and has no authentication. Any
  web page could rewrite a provider's base URL through `PUT /api/settings`, then start a run and receive the
  provider API key; it could also approve mutations, start unattended runs, and set arbitrary environment
  variables. Changes:
  - The API now serves only its own origin and loopback origins, echoing the origin instead of `*`.
  - The WebSocket accepts the same origins.
  - A loopback listener refuses non-loopback `Host` headers (DNS rebinding).
  - `/api/secrets` accepts only credential-shaped names.
  - `compose.yaml` publishes the port on `127.0.0.1` only.

  See `docs/threat-model.md` §6. Authentication is still M2.7.

### Fixed

- The event-log hash chain forked in every execution with two or more approval decisions. `wee serve`
  recorded decisions through one log instance while the resumed run appended through another, and each
  cached its own chain head. Appends are now serialized per file across instances, and a cached head is
  trusted only while the file size matches. Logs recorded before this fix still fail verification and are
  left unmodified.
- `wee replay <id>` now verifies the hash chain and fails with `integrity: FAILED` on a break. Before, the
  chain was written but never checked outside tests.
- Spend on model calls whose output the contract rejected was dropped: only the final successful attempt
  was counted, and a node that failed counted nothing. Rejected attempts now count toward the node, the
  budget, the server's run summary, the audit, the CLI's running total, and the UI. `Failure` events carry
  `costUsd`/`tokens`, and `Retry` events carry per-attempt spend.
- A resumed run started a fresh budget, so every approval pause reset the cost and token limits. A resume now
  continues the execution's recorded spend and halts before dispatch if it already exceeds a limit. Duration
  still restarts per segment.
- The node cache key did not cover a Worker's objective or constraints. Editing a `*.worker.yaml` in place
  without a version bump served the old output from cache. The key now includes the definition's hash, which
  invalidates existing cache entries once.
- Separate cache instances on one workspace (one per run, plus the server's) could drop each other's entries,
  and a run in flight could resurrect entries a "clear cache" had removed. The index is now re-read under a
  per-directory lock on every operation.
- `pr-review-autofix@1.2.1`: `push-plan@1.0.1` and `locate-file@1.0.2` add `pattern` constraints, so a
  malformed SHA, branch, or non-GitHub URL fails the contract before reaching a tool. The template bundle was
  regenerated.
- The flagship case study and README attributed run `3b8c09`'s rejection to a dropped copyright header. The
  recorded artifacts do not support that: the model fetched the head commit's file, and the verifier
  rejected an unchanged file for a wrong reason. Both documents are corrected.
- `docs/VISION.md` no longer implies built-in webhook or cron triggers. CI, cron, and hooks invoke `wee run`.
- The lint job had never succeeded on any commit: `golangci-lint` was pinned to v1.55.2, built with go1.22,
  whose typechecker cannot parse the go1.23 this module targets. Bumped to v1.64.8, the last v1 release.
- Two unchecked `json.Decoder.Decode` errors in the server tests, which would have reported a malformed
  body as a confusing assertion failure against the zero value.
- A deprecated `tar.TypeRegA` case in `wee backup restore`. `archive/tar` normalizes that flag before
  returning a header, so the branch was unreachable.
- Flaky `SettingsModal` secret save/clear tests, which chained three wall-clock waits and could exceed
  their budget under a loaded full-suite run.
- Two docs used `prompt` as a domain concept where the canonical term is `Context` or `inputs`.
