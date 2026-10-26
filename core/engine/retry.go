package engine

import (
	"context"
	"errors"
	"time"
)

// Error classification. A NodeExecutor wraps its errors so the scheduler knows
// whether to retry:
//
//   - Transient: a flaky failure (timeout, rate limit) — retry as-is.
//   - ContractViolation: output failed its contract — retry, and (M1.4) the
//     executor appends the validation feedback to the next attempt's context.
//   - Fatal (the default for any unwrapped error): do not retry; fail the node.
type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

type contractViolationError struct {
	err        error
	Feedback   string
	MaxRetries int // contract.maxRetries — the bound for THIS class, distinct from transient retries
}

func (e contractViolationError) Error() string { return e.err.Error() }
func (e contractViolationError) Unwrap() error { return e.err }

type fatalError struct{ err error }

func (e fatalError) Error() string { return e.err.Error() }
func (e fatalError) Unwrap() error { return e.err }

// Transient marks err as a retryable transient failure.
func Transient(err error) error { return transientError{err} }

// ContractViolation marks err as a contract violation, carrying the delta
// feedback to surface on the retry and the contract's own retry bound
// (contract.maxRetries) — contract-violation retries are counted separately from
// transient retries (REQ-CONTRACT-02).
func ContractViolation(err error, feedback string, maxRetries int) error {
	return contractViolationError{err: err, Feedback: feedback, MaxRetries: maxRetries}
}

// Fatal marks err as non-retryable.
func Fatal(err error) error { return fatalError{err} }

// spendError carries what a failed attempt already cost: a model call whose
// output the contract then rejected was still billed by the provider. It wraps
// a classified error without changing its class, so the engine can count the
// spend toward the node and the budget (REQ-BUDGET-03) whether or not the node
// eventually succeeds.
type spendError struct {
	err     error
	CostUSD float64
	Tokens  int64
}

func (e spendError) Error() string { return e.err.Error() }
func (e spendError) Unwrap() error { return e.err }

// WithSpend attaches a failed attempt's cost and tokens to err.
func WithSpend(err error, costUSD float64, tokens int64) error {
	if costUSD == 0 && tokens == 0 {
		return err
	}
	return spendError{err: err, CostUSD: costUSD, Tokens: tokens}
}

// spendOf returns the cost and tokens a failed attempt recorded via WithSpend.
func spendOf(err error) (float64, int64) {
	var se spendError
	if errors.As(err, &se) {
		return se.CostUSD, se.Tokens
	}
	return 0, 0
}

type errorClass int

const (
	classFatal errorClass = iota
	classTransient
	classContractViolation
)

// classify decides how an executor error should be handled. Anything not
// explicitly marked transient or contract-violation is treated as fatal.
func classify(err error) errorClass {
	var ce contractViolationError
	if errors.As(err, &ce) {
		return classContractViolation
	}
	var te transientError
	if errors.As(err, &te) {
		return classTransient
	}
	return classFatal
}

// backoffFunc returns the delay before retry attempt n (1-based). nil means no
// delay.
type backoffFunc func(attempt int) time.Duration

// exponentialBackoff returns base * 2^(attempt-1), capped at max. A zero base
// yields no delay (used in tests).
func exponentialBackoff(base, max time.Duration) backoffFunc {
	return func(attempt int) time.Duration {
		if base <= 0 {
			return 0
		}
		d := base
		for i := 1; i < attempt; i++ {
			d *= 2
			if d >= max {
				return max
			}
		}
		return d
	}
}

// withRetry runs fn until it succeeds, hits a fatal error, or exhausts the retry
// budget for the failure's class. Transient failures are bounded by maxTransient
// (the per-node retry budget); contract violations are bounded independently by
// each violation's own contract.maxRetries (REQ-CONTRACT-02) — one loop, two
// counters, so a flaky provider and a stubborn model don't share a budget.
// onRetry is called before each retry with the running attempt number and the
// classified error. A transient failure carrying a server Retry-After hint
// raises the backoff for that attempt (REQ-MODEL-05). Backoff sleeps respect ctx.
func withRetry(ctx context.Context, maxTransient int, backoff backoffFunc, fn func() error, onRetry func(attempt int, err error)) error {
	transientN, contractN := 0, 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		switch classify(err) {
		case classFatal:
			return err
		case classTransient:
			if transientN >= maxTransient {
				return err
			}
			transientN++
		case classContractViolation:
			var cve contractViolationError
			errors.As(err, &cve)
			if contractN >= cve.MaxRetries {
				return err
			}
			contractN++
		}
		attempt := transientN + contractN
		if onRetry != nil {
			onRetry(attempt, err)
		}
		d := backoff(attempt)
		if hint, ok := retryAfterOf(err); ok && hint > d {
			d = hint
		}
		if d > 0 && !sleep(ctx, d) {
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// retryAfterOf extracts a server-requested delay from a transient error without
// importing the provider layer: any error in the chain exposing RetryAfterHint
// (model.TransientError does) supplies it.
func retryAfterOf(err error) (time.Duration, bool) {
	var h interface {
		RetryAfterHint() (time.Duration, bool)
	}
	if errors.As(err, &h) {
		return h.RetryAfterHint()
	}
	return 0, false
}

// sleep waits for d or until ctx is done; it returns false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
