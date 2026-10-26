# Threat Model (v1)

**Requirement:** NFR-SEC-03 · **Status:** v1 (M1.5) — first version, alongside the built-in tools. Hardened
and red-teamed in M2.7. · **Principles:** PRIN-09, PRIN-10.

## Scope and posture

The engine's job is to run **workflow definitions** — data describing Workers, Contracts, edges, and tool
calls — and to let those Workers touch the world only through sandboxed tools. This document enumerates the
ways that boundary could be attacked and what stops each one.

The v1 posture is **deny-first**: filesystem confined to a workspace root, terminal behind a command
allowlist, HTTP behind a domain allowlist, git with no remote access. Integrity is **structural**: artifacts
are content-addressed (ADR 0004), history is hash-chained (ADR 0007). Secrets are references, never values
(NFR-SEC-01).

Two trust tiers matter:

- **Trusted definitions** (today): workflows authored by the operator running the engine. The tool sandbox
  still applies, but the definition author is not treated as an adversary.
- **Untrusted definitions** (Phase 2 — registry/templates/hosting): workflows from third parties. Every
  mitigation below must hold against a definition written to attack the host. Items tagged _(Phase 2)_ are
  not fully closed in v1 and gate that transition.

## Threats and mitigations

### 1. Tool sandbox escape

An attacker-controlled tool input tries to reach outside the tool's sandbox — e.g. the filesystem tool
reading `/etc/passwd`, or writing outside the workspace.

- **Filesystem** (`core/tool/filesystem`): absolute paths are rejected; `.` / `..` are neutralized by
  cleaning; symlinks are resolved on the longest existing prefix and the result must stay within the
  symlink-resolved root, so a symlinked ancestor cannot smuggle a path out. Verified by
  `TestPathTraversalRejected`, `TestSymlinkEscapeRejected`.
- **Terminal** (`core/tool/terminal`): only `argv[0]` values on the per-workflow allowlist run; commands
  execute in the workspace dir under a timeout. It does **not** yet sandbox what an allowlisted command
  itself does (a permitted `sh` can do anything `sh` can) — operators must allowlist narrow, trusted
  commands. OS-level sandboxing (namespaces/seccomp) is _(Phase 2)_.
- **Ships:** M1.5. **Residual:** allowlisted-command blast radius (Phase 2 OS sandbox).

### 2. Allowlist bypass

An attacker crafts a tool call that slips past the allowlist — a command name with padding, a URL whose
real host differs from what the check reads, a redirect to a denied host.

- **Terminal**: the allowlist is matched on the exact `argv[0]`; there is no shell string to smuggle a
  second command through (args are passed as a list, never concatenated into a shell line).
- **HTTP** (`core/tool/http`): the host is taken from the parsed URL (`url.Hostname()`), matched
  exactly or by explicit subdomain suffix (`.example.com`); an empty allowlist denies everything. Verified
  by `TestDisallowedDomainRejected`, `TestEmptyAllowlistDeniesAll`.
- **Residual _(Phase 2)_:** HTTP redirects are followed by the stdlib client and are **not** re-checked
  against the allowlist per hop — a permitted host redirecting to a denied one is not yet blocked. Closing
  this needs a redirect policy on the client. Also SSRF to link-local/metadata IPs is not yet blocked.
- **Ships:** M1.5 (name/host checks). **Residual:** redirect re-checking, SSRF IP filtering (Phase 2).

### 3. Malicious definitions

A hostile workflow/worker/contract definition tries to harm the host through the parser or the type system
rather than a tool.

- **Schema bombs** (deeply nested / huge JSON to exhaust memory): definitions and event lines are size- and
  structure-validated against fixed JSON Schemas (`core/validate`); the event reader caps line size
  (`maxLine`). A dedicated input-size / nesting-depth limit on definition loading is _(Phase 2)_.
- **Path traversal via artifact "type" or ids**: artifact identity is the content hash, not a
  caller-supplied path (`core/store`, ADR 0004) — a definition cannot choose where bytes land on disk, so a
  crafted id/type cannot write outside the store. Artifact `type` is a closed enum (`domain.ArtifactType`).
- **Contract output schemas** are compiled in isolation and only validate a node's own output; a malicious
  schema can reject its Worker's output but cannot reach beyond that node.
- **Ships:** M1.5 (structural mitigations already in place from M1.1–M1.4). **Residual:** explicit
  definition size/depth limits (Phase 2).

### 4. Secret exfiltration via tool calls

A Worker tries to read a secret (API key, token) and leak it — into an artifact, an HTTP body, or a commit.

- Secrets are **references**, never values (NFR-SEC-01): API keys live in env/keychain and are read only
  inside provider clients, which never log request headers and never place key material in events,
  snapshots, artifacts, or exports. Verified by `openai.TestNoKeyMaterialInExecutionRecord`,
  `TestNoHeaderInError`.
- The engine does not inject secrets into a Worker's context; a Worker has no `env`/secret accessor tool in
  v1. The HTTP tool sends only the headers a call explicitly sets, and its domain allowlist bounds where any
  data could be sent.
- **Residual _(Phase 2)_:** a Worker with a broad HTTP allowlist and access to sensitive artifacts could
  still POST them outward — mitigated by narrow allowlists and context policies (REQ-CTXPOL), fully
  addressed by the untrusted-definition review (M2.7).
- **Ships:** M1.4 (secret hygiene), M1.5 (tool boundary).

### 5. Event-log poisoning

An attacker tampers with an execution's record — editing a result, deleting a step, reordering — to forge a
clean audit or a false savings report.

- The event log is **hash-chained** (ADR 0007, REQ-EVENT-03): each event carries the hash of the previous
  event's raw bytes; `eventlog.Verify` walks the chain and names the first break, catching any edit,
  deletion, or reorder — including injected fields the struct would otherwise ignore. Verified by
  `TestVerifyDetectsTamper`, `TestVerifyDetectsGenesisBreak`.
- `wee replay <id>` (the audit) runs `Verify` and fails with `integrity: FAILED` on a break, so an audit
  is a check, not only a rendering. Verified by `TestReplayAuditFailsOnTamperedLog`.
- Appends are serialized per events file across every `eventlog.Log` instance in the process, and a cached
  chain head is trusted only while the file size still matches it. Before this (fixed 2026-10-25), `wee
  serve` recorded approval decisions through one instance while resumed runs appended through another; each
  cached its own head, so every execution with two or more approval decisions forked its own chain. Those
  recorded logs fail verification and are deliberately left unmodified. Verified by
  `TestVerifyCleanAcrossLogInstances`, `TestVerifyCleanUnderConcurrentInstances`.
- Artifacts are content-addressed, so altering an artifact changes its hash and breaks the reference.
- **Residual _(neutral, by design):** the chain proves *internal* consistency; a party who rewrites the
  whole chain from a break point onward defeats it. External anchoring (publishing head hashes) is a
  possible Phase 2 hardening, not promised now. Appends from separate *processes* (a `wee run` and a `wee
  serve` on one execution) are not locked against each other.
- **Ships:** M1.4 (chain retrofit); audit verification and multi-instance appends fixed 2026-10-25.

### 6. Local control plane (`wee serve`)

A web page the operator visits while `wee serve` is running — or, for the Docker image, any host that can
reach the published port — calls the HTTP API directly. The API has no authentication yet (planned for
M2.7). Until 2026-10-25 it also answered every origin with `Access-Control-Allow-Origin: *`, so a page on any
origin could rewrite a provider's base URL through `PUT /api/settings` and start a run with `POST /api/run`.
The provider client then sent the API key to the page's server. The same path could approve mutation
checkpoints, start runs with `allowMutationsWithoutApproval`, and set arbitrary environment variables
(`GOFLAGS`, `GIT_*`, proxies) inherited by tool subprocesses.

- **Origin policy:** a request carrying an `Origin` is served only from the server's own origin (the
  `--ui-dir` build) or a loopback origin (the Vite dev server); every other origin gets `403` before any
  handler runs. Allowed origins are echoed, never `*`. The WebSocket event stream admits the same origins.
  Requests with no `Origin` (curl, the CLI) are unaffected. Verified by `TestCrossOriginRequestsRefused`,
  `TestSameOriginAndNoOriginAllowed`.
- **DNS rebinding:** when `wee serve` listens on a loopback address (the default, `127.0.0.1:7676`), a
  request whose `Host` is not a loopback name is refused. Without this, a domain re-resolved to 127.0.0.1
  would be same-origin with the server. Verified by `TestIsLoopbackHostAndAddr`.
- **Secrets endpoint:** `POST`/`DELETE /api/secrets` accept only credential-shaped names (`UPPER_SNAKE`
  ending in `_KEY`, `_TOKEN`, `_AUTH_HEADER`, `_SECRET`, `_PASSWORD`), never process-control variables.
  Verified by `TestSetSecretRejectsNonCredentialNames`.
- **Docker:** `compose.yaml` publishes the port on `127.0.0.1` only. The container itself must listen on
  `0.0.0.0`, so the Host check does not apply inside it; publishing the port beyond loopback exposes an
  unauthenticated API to that network.
- **Residual _(Phase 2, M2.7)_:** no authentication. Any local process, and any client that can reach a
  non-loopback listen address without a browser, has full control of the API.
- **Ships:** origin policy, rebinding guard, secret-name bound, compose binding — 2026-10-25.

## Summary

| Threat | Primary mitigation | Ships | Residual (Phase 2) |
|---|---|---|---|
| Tool sandbox escape | root confinement, command allowlist, timeouts | M1.5 | OS-level sandbox for allowlisted commands |
| Allowlist bypass | exact argv / parsed-host matching, deny-first | M1.5 | redirect re-checking, SSRF IP filtering |
| Malicious definitions | schema validation, content-addressed storage, closed enums | M1.1–M1.5 | definition size/depth limits |
| Secret exfiltration | secrets as references, no header logging, no secret tool | M1.4–M1.5 | broad-allowlist review (M2.7) |
| Event-log poisoning | hash-chained log, content-addressed artifacts, verified audit | M1.4 | external anchoring, cross-process append lock |
| Local control plane | origin allowlist, loopback Host guard, credential-name bound, loopback-only compose port | 2026-10-25 | authentication (M2.7) |

Phase 2 hardening and a red-team pass are tracked under NFR-SEC-03 (M2.7).
