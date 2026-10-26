# Spec — Budgets

**Prefix:** `REQ-BUDGET` · **Status:** partially DELIVERED (M1.3; real-cost wiring M1.4) · **Principles:**
PRIN-05 · **Implementation:** `core/engine/budget.go`, `core/cost/` (M1.4)

Every execution declares a budget: max cost (USD), max tokens, max duration, max retries per node. The
runtime enforces it and fails fast with a clear event. **No silent $40 executions.** A zero limit means
unlimited (explicitly chosen, not defaulted into).

### REQ-BUDGET-01 — Enforcement before spend
The engine shall check the budget before each node dispatch and before each model call, and shall halt the
execution the moment a limit is crossed — emitting `BudgetExceeded` and returning the distinct
`ErrBudgetExceeded` (CLI exit code 2).
- **Rationale:** PRIN-05 — the halt happens *before* the next spend, not after the invoice.
- **Delivered by:** M1.3 (halt mechanics), M1.4 (real cost feed). **Verified by:**
  `TestBudgetExceededHalts`; _pending_ M1.4 real-cost test.

### REQ-BUDGET-02 — Early warning
When cumulative spend crosses 80% of any budget dimension, the engine shall emit `BudgetWarning` exactly
once for that dimension.
- **Delivered by:** M1.3. **Verified by:** covered within scheduler tests (warn threshold in
  `budgetTracker`); explicit test _pending_ M1.4.

### REQ-BUDGET-03 — Accurate per-call accounting
When a model call completes, the engine shall record input/output tokens and compute cost from the
provider's published rates, aggregated per node and rolled up per execution (feeding REQ-METRIC-01). This
holds whether or not the call's output clears its contract: a rejected attempt was still billed.
- **Implementation note:** a rejected attempt's spend travels with its error (`engine.WithSpend`).
  - A node that later succeeds reports the total of all its attempts on `WorkerFinished`.
  - A node that fails reports the spend of its rejected attempts on `Failure` (`costUsd`/`tokens`).
  - `Retry` carries each attempt's `attemptCostUsd`/`attemptTokens`, for information only.

  Before 2026-10-25, only the final, successful attempt was counted. A call cut off by cancellation or a
  transport error returns no usage, so it still cannot be priced.
- **Delivered by:** M1.4 (`core/cost/accounting.go`). **Verified by:** `cost` package tests; real per-call
  cost surfaced through `NodeResult.CostUSD` (`engine.TestNoMalformedOutputCrossesBoundary` asserts it is
  priced); `engine.TestRejectedAttemptSpendIsCounted`, `engine.TestFailedNodeSpendIsCounted`.
