# Contributing

Thanks for looking. Before anything else, know what kind of project this is: `wee` is **spec-driven and
milestone-sequential**. Behavior is specified in [docs/spec/](docs/spec/) before it is built, milestones run
in order, and scope decisions belong to the project owner. That makes some contributions easy to accept and
others easy to decline — this page exists so you know which is which before you spend an evening on it.

## Read first

1. [docs/CONSTITUTION.md](docs/CONSTITUTION.md) — the binding rules. Changes only via a recorded ADR.
2. [AGENTS.md](AGENTS.md) — the index: what to read, in what order, and where the current state lives.
3. [docs/EXECUTION-PHASE2.md](docs/EXECUTION-PHASE2.md) — its `## Status` section names the **active
   milestone**. That is the resumable state of the project.

## Propose before you build

The constitution's third process law is "never invent scope." In practice:

- **Bug fix or chore?** Open a PR directly. Small, self-contained, obviously correct — go ahead.
- **Anything else?** Open an issue first. A feature that the roadmap and specs don't already ask for won't
  be merged just because it works; it needs to be placed in the plan first. That's not a judgment on the
  idea, only on its timing.
- **New third-party dependency?** Present findings and a recommendation; the decision is the owner's, per
  PRIN-07. Never a unilateral swap.
- **Irreversible or contested technical choice?** It needs an [ADR](docs/adr/) before it's pinned.

## Gates

Everything below runs in CI as three independent workflows. Run them locally first — none of them need
network access or an API key:

```sh
go build ./... && go vet ./... && go test ./... -race
make vocab
cd ui && pnpm install && pnpm lint && pnpm typecheck && pnpm test && pnpm build
```

`make vocab` is the PRIN-04 vocabulary gate. `Prompt`, `Agent`, `Chat` and `Memory` must not be used as
domain concepts in `schemas/`, `core/`, `cli/`, `sdk/` or `docs/` — use `Contract`, `Worker`, `Workspace`,
`Artifacts` and `Context` instead. See [docs/glossary.md](docs/glossary.md) for the full table, and
[scripts/check-vocabulary.sh](scripts/check-vocabulary.sh) for the narrow exemptions and how to mark a
legitimate non-domain use.

## Commits

One commit per **logical unit of work** — a package, a bug fix, a doc pass. Not a squashed milestone, and
not a file-by-file trickle. Each message should stand on its own for `git blame` and `git bisect` without
needing the rest of the branch as context, and where feasible each commit should build and pass its own
tests.

```text
M2.7: <summary>     milestone-scoped work
fix: <summary>      a bug found along the way
ci: / chore: / docs: <summary>
```

## Tests

New behavior needs a test that fails without the change. Requirements carry stable IDs (`REQ-*`, `NFR-*`);
when your change delivers or verifies one, name it — in the spec entry, and in the test that proves it.
