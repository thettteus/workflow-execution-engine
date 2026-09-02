# AGENTS.md — Workflow Execution Engine

This file is a map, not a manual. It exists so a coding agent (or a human) can open this repo cold and
know where to look. It should stay short — if it grows past ~150 lines, cut it back and push detail into
the doc it points to.

> Naming note: this file's name follows the cross-tool `AGENTS.md` convention (Codex, Cursor, and others
> read it by default). It is the one deliberate exception to PRIN-04's forbidden-vocabulary rule below —
> the rule governs how this project describes *itself* (`schemas/`, `core/`, `cli/`, `sdk/`, `docs/`); it
> does not extend to the filename a third-party harness expects to find at the repo root. `CLAUDE.md`
> imports this file verbatim (`@AGENTS.md`) so Claude Code reads the same index without a second copy.

## What this project is

A **workflow execution engine** — a governance layer that turns engineering processes into versioned,
auditable, replayable software. It is explicitly **not** an AI agent framework, not a chat product, not a
prompt builder. If you're about to describe it that way, stop and read
[docs/CONSTITUTION.md](docs/CONSTITUTION.md) PRIN-04 first.

## Read in this order

1. [docs/CONSTITUTION.md](docs/CONSTITUTION.md) — the laws (PRIN-01..10). Binding, changes rarely, only via
   a recorded ADR.
2. [docs/VISION.md](docs/VISION.md) — why the project exists, positioning, business model. Non-normative.
3. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the as-built/as-planned component map and execution
   lifecycle, as diagrams. Non-normative; when it drifts from `core/`, the code wins — fix the diagram.
4. [docs/spec/README.md](docs/spec/README.md) — the testable **what**: one file per capability, `REQ-*`/
   `NFR-*` IDs, EARS-format requirements. This is the source of truth on behavior; prose docs defer to it.
5. [docs/ROADMAP.md](docs/ROADMAP.md) — the **when**: Phase 1 MVP and Phase 2 local-first product
   hardening, with milestones and `Delivers:` lines mapping milestones to requirement IDs where applicable.
6. [docs/EXECUTION.md](docs/EXECUTION.md) — the **how**: task-by-task playbook for Phase 1 (M1.0–M1.17).
   **Read its `## Status` section first** — it names the current milestone and is the resumable state of
   the project. Do not start a milestone whose predecessor isn't checked off and verified.
7. [docs/EXECUTION-PHASE2.md](docs/EXECUTION-PHASE2.md) — the next active product-hardening playbook once
   Phase 1 is closed or explicitly superseded by the owner.
8. [docs/adr/](docs/adr/) — irreversible decisions (language/runtime, serialization, content-addressing,
   contract validation, model-provider integration, event-log hash-chain). A pinned ADR is not
   re-litigated mid-implementation.
9. [docs/glossary.md](docs/glossary.md) — canonical vocabulary and the forbidden-AI-vocabulary table.
10. [.claude/skills/agent-harness-design/SKILL.md](.claude/skills/agent-harness-design/SKILL.md) —
   harness-design principles this AGENTS.md/CLAUDE.md setup itself follows (source: Anthropic + OpenAI,
   2026). Invocable directly as a Claude Code skill. The decision to structure docs this way is
   [ADR 0001](docs/adr/0001-harness-oriented-docs.md).

## Process laws (binding — see CONSTITUTION.md §Process laws for the full text)

- Milestone-driven, sequential. No skipping ahead.
- Commit by logical unit of work, not by milestone squash and not by file: prefix milestone-scoped commits
  `M<phase>.<n>: <summary>`; a fix or chore found along the way gets its own conventional prefix (`fix:`,
  `chore:`) instead.
- Never invent scope — if ROADMAP/spec don't ask for it, it doesn't belong in the current phase.
- Every user-requested product/code change must be checked against the active milestone before
  implementation. If it is not already covered, first propose adding it to the active execution plan (or
  explicitly classify it as a narrow bugfix/chore) and wait for the owner's scope decision.
- Irreversible or contested choices get an ADR before they're pinned.
- Third-party dependencies are vetted (PRIN-07) before entering `go.mod` — findings + recommendation
  presented, decision is the project owner's, never a unilateral swap.
- Every requirement carries a stable `REQ-*`/`NFR-*` ID; milestones and acceptance tests cite the IDs they
  deliver/verify.

## Forbidden vocabulary (PRIN-04)

`Prompt`, `Agent` (as a domain concept), `Chat`, `Memory` must not appear in `schemas/`, `core/`, `cli/`,
`sdk/`, or `docs/` outside the glossary's "instead-of" table. Use `Contract`, `Worker`, `Workspace`,
`Execution`/`Artifacts`/`Context` instead. Enforced in CI by `scripts/check-vocabulary.sh`
(`.github/workflows/vocabulary.yml`, or `make vocab` locally). The gate exempts the files whose
subject *is* the rule or the positioning against it, plus documented technical senses like
`in-memory` and OpenAI's `chat/completions`; a one-off legitimate use is marked inline with
`vocab-allow`. Originally specified as a manual grep in EXECUTION.md's M1.0 acceptance criteria.

## Non-goals — see CONSTITUTION.md for the full list

Chat interface · RAG · vector DBs · multi-tenancy/billing/marketplace · fine-tuning · autonomous
long-running loops · knowledge bases.

## Repo layout

`core/` (Go engine) · `cli/` (`wee` binary) · `sdk/` (Go authoring SDK) · `ui/` (React/TS, client of the
event stream) · `schemas/` (JSON Schema, draft 2020-12 — language-neutral source of truth) · `examples/`
(demo workflow definitions) · `docs/` (everything above).
