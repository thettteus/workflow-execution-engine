# Vision — Workflow Execution Engine

Repo: `workflow-execution-engine` · Binary: `wee`

> Non-normative. This document says **why** the project exists and where it's going. The laws live in
> [CONSTITUTION.md](CONSTITUTION.md); the testable **what** lives in [spec/](spec/); the **when** in
> [ROADMAP.md](ROADMAP.md); the **how** in [EXECUTION.md](EXECUTION.md).

## Premise

The goal is **not** to build another AI agent framework.

The goal is a platform that transforms human engineering processes into executable, auditable, observable
workflows. Today, engineering knowledge lives in documents, Slack conversations, and people's heads. This
project turns those processes into software.

> **Workflows are software.**

They have: source code, versioning, execution, replay, artifacts, observability, metrics, auditability.

LLMs are an implementation detail, not the product.

---

## Positioning

### The discipline layer for models built to spend

Tokens got cheaper per unit — and total spend went up anyway, because agentic products exploded the
*volume*: unbounded loops, re-inflated context, verbose output nobody consumes. The incentives are
misaligned: model vendors profit from every extra token; no one in the stack is on the side of the user's
invoice.

This engine is that counterweight — **a governance layer that imposes engineering discipline on models
programmed to spend**:

- The **engine owns the loops**, not the model. Retries, branching, merging are deterministic control
  flow; the model is never asked to burn tokens deciding "should I try again?"
- **Context is rationed** by declared policy — the diff, not the repo (spec: [context-policies](spec/context-policies.md)).
- **Output is contracted** — tight schemas, bounded lists, enforced not suggested; retries feed back the
  errors, never a re-grown transcript (spec: [contracts](spec/contracts.md)).
- **Work is never paid for twice** — content-addressed caching across runs (spec: [cache](spec/cache.md)).
- **Spend is fenced** — budgets halt before the next call, not after the invoice (spec: [budgets](spec/budgets.md)).
- **Savings have receipts** — avoided spend is attributed to its cause, auditable from the event log
  (spec: [metrics](spec/metrics.md)).

### Neighborhood

Closer to GitHub Actions, Temporal, Raycast, Linear, VS Code, Figma — than to LangFlow, Flowise, CrewAI
playgrounds, or prompt builders. The product is about **engineering systems**, not chatting with AI.

## What WEE is not

WEE is not an integration platform like n8n or Zapier.

Those systems automate APIs and business processes.

WEE executes engineering processes.

A pull request review, an architecture review, or a product requirements workflow is not a chain of API calls—it is a versioned, observable, replayable program whose workers may happen to include language models.

Likewise, WEE is not an AI agent framework. Models are workers inside the runtime, not the runtime itself.

### Where WEE lives

In the places engineering work already happens — never in a chat window:

- **CI** — a pipeline step runs `wee run` against a change-review workflow and branches on its exit code;
  the result lands wherever that integration chooses. (There is no built-in webhook receiver yet.)
- **The terminal** — `wee run` locally, like git or terraform.
- **Cron / hooks** — the system scheduler or a git hook invokes `wee run` for scheduled research digests or
  pre-push review gates. (There is no built-in scheduler or trigger store yet; runs live in the process that
  started them.)
- **The editor's neighbor** — the UI is a client of the same event stream the CLI prints.

---

## Who it's for

WEE is built for the people who own engineering processes and answer for their cost and quality:

- **Developers** run it like git or terraform — a PR review, test generation, or refactor plan from the
  terminal or the UI, against a local diff or a public patch, for a few cents, with the receipts to prove it.
- **Scrum Masters / delivery leads** encode recurring ceremonies and gates (release notes, change risk,
  definition-of-done checks) as versioned workflows instead of tribal checklists.
- **Product Owners / Product Managers** run research → PRD → review workflows that produce auditable,
  contracted artifacts — not a chat transcript.
- **CTOs / engineering leaders** get what they actually buy on: reproducible process, bounded and attributed
  spend, a tamper-evident record, and nothing leaving their environment. The savings have receipts.

The through-line: everyone is **programming their organization's processes**, not prompting a model. For any
of them to adopt it, the product has to be good enough to notice on sight — which is the experience bar below.

---

## Product Philosophy

The product answers one question: **how do we execute knowledge?**

Instead of documenting `Implement → Review → Fix → Merge`, the team defines a workflow that actually
executes that process — versioned, observable, replayable, budgeted.

The design principles that govern every feature — reproducible & auditable, observable, composable,
engineering-first, token economy, minimalism, ownership, anti-slop, tamper-evidence, secure by default —
are law, not aspiration: [CONSTITUTION.md](CONSTITUTION.md) (PRIN-01…10).

---

## The experience bar

A governance layer only governs what people actually use. The local product must therefore be **inevitable
to notice and trivial to pilot** — professional enough that a developer keeps it open and a CTO is
comfortable standing behind it. Concretely (decision [ADR 0015](adr/0015-ui-shell-and-visual-system.md);
requirements in [spec/ui.md](spec/ui.md)):

- **Expressive, disciplined craft.** The interface is premium — depth, restrained gradients, purposeful
  motion, light and dark — anchored to the Linear / Vercel / Figma neighborhood, never a consumer AI toy.
  Impact through craft, bounded by accessibility (WCAG AA) and performance (200-node canvas).
- **Pilotable at a glance.** One status/signal system (color *and* icon *and* label), dashboard-grade KPIs,
  and a command-palette spine make state and cost legible in seconds.
- **Guided, not gatekept.** A first-time user reaches a first successful run from an empty canvas without
  reading docs; docs are one keystroke away when they want them.
- **Private by default.** No silent telemetry or phone-home; secrets are references, never values, everywhere
  they could leak.

External systems reach the product as **connections** — named, non-secret references that workflows consume:
model providers (OpenAI, Anthropic, Kimi, self-hosted) and change sources (GitHub, GitLab, Bitbucket, local
repositories). They let WEE work everywhere a team already does, without any of those integrations becoming
part of the engine (decision [ADR 0013](adr/0013-connections-model.md)). Long or backgrounded runs surface
their completion through local, configurable notifications ([ADR 0014](adr/0014-notifications-model.md)), so
a run is never silently forgotten.

---

## Architecture at a glance

> Diagrams (component map, execution lifecycle) live in [ARCHITECTURE.md](ARCHITECTURE.md); this section
> stays prose-only.

The **Core is the product**; the interface is one possible client. CLI-first, API-first, SDK-first — the
entire platform is usable without any UI.

- **Core, CLI, SDK: Go.** Single static binary, goroutine-native scheduler, git/terraform-grade
  distribution.
- **Contracts: JSON Schema** (draft 2020-12) — `schemas/` is the language-neutral source of truth.
- **Model providers: hand-rolled `net/http`, no vendor SDKs** (ADR 0006). Anthropic + OpenAI from M1.4,
  OpenAI default; any OpenAI-compatible endpoint (Ollama, vLLM) works via a base-URL override — self-hosted
  models are first-class (spec: [model-providers](spec/model-providers.md)).
- **Interface: React + TypeScript**, a pure client over the engine's event stream (`wee serve`).
- **Hosted runtime (commercial): the same Go binary** in distroless containers.

Two languages, one boundary: Go below the event stream, TypeScript above it.

Every capability is specified with testable requirements in [spec/](spec/README.md): runtime, definition,
workers, contracts, context policies, model providers, cache, budgets, artifacts, events, replay,
versioning, tools, CLI, SDK, metrics, security, UI.

---

## Distribution & Business Model

The Core is open source. Forever. BYO API key.

**The economy mechanisms are the soul of the product and stay OSS** — budgets, cache, context policies,
savings accounting all live in the free core. Paywalling the discipline would invert the positioning: we'd
become the thing we're the counterweight to. Individual developers running the CLI locally never pay; that
audience is distribution, not revenue.

The product starts local/self-hosted. A developer downloads `wee`, runs the service on their machine or a
team-owned VM, points it at their own provider keys or local models, and keeps source code, artifacts, cache,
and execution history inside their own environment. That is not a demo mode; it is the primary product
shape.

Hosted execution is a later convenience layer, not the thesis. Revenue comes from operating the same product
for teams that do not want to run it themselves, plus coordination features that become more valuable when a
team shares workflows:

### Tier 1 — Managed Runtime (usage-based)

- Hosted operation of the same runtime
- Shared node cache (one person's execution warms the whole team's cache)
- Managed API keys with margin on compute/tokens
- Execution history retention

### Tier 2 — Team (per seat, ~$15–20/seat/month)

- Everything in Managed Runtime
- **Savings report** — the managed proof of the economy the OSS core delivers: avoided-spend dashboards
  (cache, context pruning, engine-owned loops) per team/period, built on the auditable savings accounting
  of the core (REQ-METRIC-03). Credible precisely because the event log is tamper-evident.
- Workflow sharing & permissions, shared template library, execution sharing, version comparison, RBAC

**Future option (enterprise):** savings-share pricing — a percentage of audited avoided spend. Only viable
because savings have receipts.

### What is explicitly NOT the model

- ~~Cheap consumer subscription ($1.99–$3.99/mo)~~ — signals a toy, converts nobody who matters.
- Paywalling core features — kills adoption, kills the positioning, kills portfolio value.

Sequencing: **portfolio first, revenue later.** The commercial layer must never distort the Core.

---

## Flagship Demo

One demo must sell the entire project in under 3 minutes, on a real repository, with a verifiable result.

### Change Review & Auto-Fix

```text
Change Source (local diff, patch URL, PR/MR, or file set)
  ├─ Reviewer A   (diff only — style & correctness)
  ├─ Reviewer B   (diff only — assumes the code is wrong)
  └─ Security Rev (diff only — vulnerabilities)      ← all three in parallel
        ↓
      Fixer        (reads reviews + diff)
        ↓
      Test Runner  (tool: terminal)
        ↓
      Commit
```

Why this demo: minutes not hours; verifiable result (tests pass, diff readable); every differentiator at
once — parallel graph, context policies (diff only), contract enforcement (structured reviews), artifacts,
cache (tweak one reviewer → only downstream re-runs), budget, timeline.

GitHub is only one source adapter for the demo, not a product boundary. The same graph should work from a
local `git diff`, a public patch URL, GitLab, Bitbucket, or a self-hosted forge by changing workflow/tool
configuration, not Core.

Secondary demos (docs, not pitch): Bug Investigation (logs → hypothesis → patch → tests → review),
Product Requirements (research → PM → architect → reviewer → PRD), Architecture Review (spec → backend →
frontend → security → performance → merge).

---

## Long-Term Vision

The ambition is to become the **execution layer for knowledge work**.

Just as Git turned source code into versioned, collaborative assets, and GitHub Actions turned CI/CD into
executable workflows, this project turns engineering, product, research, and operational knowledge into
executable, auditable, reproducible systems.

The success criterion is not having the most capable AI. It is enabling organizations to encode how they
work into workflows that are observable, composable, versioned, cost-controlled, and reusable.

### Self-authoring workflow

WEE should eventually dogfood itself for workflow creation. A user should be able to open a guided Workspace,
describe the process they want to execute, and have WEE run an internal authoring Workflow that proposes the
first draft: Workflow graph, Worker names and objectives, Tool nodes, Context Policies, Contracts, model
provider/model choices, budgets, and the minimal connections the graph needs.

This must stay inside the product's discipline:

- The output is a versioned Workflow bundle the user can inspect, edit, validate, export, and run.
- Suggested Workers and Contracts are drafts, not magic; the UI shows why each Context Policy, Tool, model,
  and budget was chosen.
- Cost and context are bounded during authoring just as they are during execution.
- The authored Workflow is auditable from the moment it is generated: definitions carry versions and hashes,
  and subsequent changes happen through the same authoring surfaces as hand-built Workflows.

In other words: WEE may help users create Workflows conversationally, but the durable artifact is always
software — a governed, replayable process definition — never an unstructured transcript.

**The product should make users feel they are programming organizations, not prompting models.**
