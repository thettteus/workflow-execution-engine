package engine

import (
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// warnThreshold is the fraction of a limit at which a BudgetWarning fires.
const warnThreshold = 0.8

// budgetTracker accumulates spend against a Budget and reports when a warning
// or hard-stop threshold is crossed. A limit of 0 in any dimension means "no
// limit" for that dimension. Not safe for concurrent use; the scheduler updates
// it only from its coordinator goroutine.
type budgetTracker struct {
	limit  domain.Budget
	start  time.Time
	now    func() time.Time
	cost   float64
	tokens int64
	warned bool
}

func newBudgetTracker(limit domain.Budget, now func() time.Time) *budgetTracker {
	if now == nil {
		now = time.Now
	}
	return &budgetTracker{limit: limit, start: now(), now: now}
}

// add records the spend of one completed node.
func (b *budgetTracker) add(cost float64, tokens int64) {
	b.cost += cost
	b.tokens += tokens
}

func (b *budgetTracker) elapsedMs() int64 {
	return b.now().Sub(b.start).Milliseconds()
}

// exceeded reports whether any limit has been reached (spend >= limit).
func (b *budgetTracker) exceeded() bool {
	if b.limit.MaxCostUSD > 0 && b.cost >= b.limit.MaxCostUSD {
		return true
	}
	if b.limit.MaxTokens > 0 && b.tokens >= b.limit.MaxTokens {
		return true
	}
	if b.limit.MaxDurationMs > 0 && b.elapsedMs() >= b.limit.MaxDurationMs {
		return true
	}
	return false
}

// shouldWarn reports whether any limit has crossed warnThreshold, firing at
// most once for the lifetime of the tracker.
func (b *budgetTracker) shouldWarn() bool {
	if b.warned {
		return false
	}
	hit := false
	if b.limit.MaxCostUSD > 0 && b.cost >= warnThreshold*b.limit.MaxCostUSD {
		hit = true
	}
	if b.limit.MaxTokens > 0 && float64(b.tokens) >= warnThreshold*float64(b.limit.MaxTokens) {
		hit = true
	}
	if b.limit.MaxDurationMs > 0 && float64(b.elapsedMs()) >= warnThreshold*float64(b.limit.MaxDurationMs) {
		hit = true
	}
	if hit {
		b.warned = true
	}
	return hit
}

// status is the payload shared by BudgetWarning / BudgetExceeded events.
func (b *budgetTracker) status() map[string]any {
	return map[string]any{
		"spentCostUsd":  b.cost,
		"spentTokens":   b.tokens,
		"elapsedMs":     b.elapsedMs(),
		"maxCostUsd":    b.limit.MaxCostUSD,
		"maxTokens":     b.limit.MaxTokens,
		"maxDurationMs": b.limit.MaxDurationMs,
	}
}

// recordedSpend sums the cost and tokens an execution's log already records —
// WorkerFinished totals plus the spend Failure events carry for nodes whose
// attempts were billed but rejected — and whether a BudgetWarning already
// fired, so a resumed segment continues the execution's budget instead of
// starting a fresh one. An unreadable log contributes nothing.
func (s *Scheduler) recordedSpend(execID string) (cost float64, tokens int64, warned bool) {
	events, err := s.log.ReadAll(execID)
	if err != nil {
		return 0, 0, false
	}
	for _, ev := range events {
		switch ev.Type {
		case domain.WorkerFinished, domain.Failure:
			if c, ok := ev.Payload["costUsd"].(float64); ok {
				cost += c
			}
			if t, ok := ev.Payload["tokens"].(float64); ok {
				tokens += int64(t)
			}
		case domain.BudgetWarning:
			warned = true
		}
	}
	return cost, tokens, warned
}
