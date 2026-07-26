// Package engine is the workflow runtime: it walks the graph, runs independent
// nodes in parallel on a bounded goroutine pool, retries, enforces budgets,
// handles per-node failure policies and conditional edges, and supports
// cancellation and resume. It owns orchestration; a NodeExecutor (M1.4) owns
// what a node actually does. Every observable step is written to the event log
// (core/eventlog), so an execution is fully reconstructable from disk.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/diagnostic"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// DefaultConcurrency is the worker-pool size when RunOptions.Concurrency is 0.
const DefaultConcurrency = 4

// CacheMode controls how a run uses the node cache (REQ-CACHE-04).
type CacheMode string

const (
	// CacheOn reads hits and writes new entries. The zero value normalizes to
	// this, so a run caches by default.
	CacheOn CacheMode = "on"
	// CacheOff bypasses the cache entirely — no reads, no writes, no events.
	CacheOff CacheMode = "off"
	// CacheReadOnly serves hits but never records new entries (e.g. replay).
	CacheReadOnly CacheMode = "readonly"
)

// Sentinel errors returned by Run/Resume. Callers (and the CLI's exit codes in
// M1.9) branch on these with errors.Is.
var (
	ErrBudgetExceeded  = errors.New("engine: budget exceeded")
	ErrCancelled       = errors.New("engine: execution cancelled")
	ErrNodeFailed      = errors.New("engine: node failed")
	ErrIncompleteGraph = errors.New("engine: workflow graph did not fully resolve (cycle or unreachable node?)")
)

// NodeState is a node's terminal (or in-progress) state within a run.
type NodeState string

const (
	StatePending   NodeState = "pending"
	StateRunning   NodeState = "running"
	StateSucceeded NodeState = "succeeded"
	StateFailed    NodeState = "failed"
	StateSkipped   NodeState = "skipped"
)

// NodeOutcome is a node's result within an execution.
type NodeOutcome struct {
	State   NodeState
	Hash    string
	Type    domain.ArtifactType
	CostUSD float64
	Tokens  int64
	Err     string
}

// Result summarizes a finished (or halted) execution. It is always returned,
// even alongside a non-nil error, so callers can inspect partial progress.
type Result struct {
	ExecutionID    string
	State          domain.ExecutionState
	Nodes          map[string]NodeOutcome
	SpentCostUSD   float64
	SpentTokens    int64
	BudgetExceeded bool
}

// RunOptions configures a single execution.
type RunOptions struct {
	ExecutionID     string
	Concurrency     int           // worker-pool size; 0 → DefaultConcurrency
	Budget          domain.Budget // 0 in any dimension means "no limit"
	RetryBackoff    time.Duration // base backoff between retries; 0 → no delay
	RetryBackoffMax time.Duration // cap; 0 → 30s
	Cache           CacheMode     // "" → CacheOn
	// DefinitionHashes pins the content hash of each definition this run used
	// (worker "id@version" → canonical hash), recorded verbatim in the snapshot
	// so replay resolves definitions from the pinned record, not the current
	// registry state (REQ-VERSION-02). The engine treats it as opaque provenance
	// — a caller (the registry, via DefinitionHashes(wf)) computes it. Optional:
	// nil leaves the snapshot byte-identical to a run that never pinned anything.
	DefinitionHashes map[string]string
	// Workers pins the full resolved Worker definition (goal, contract,
	// contextPolicy) behind each "id@version" DefinitionHashes also names —
	// recorded in the snapshot so the Inspector (M1.13, REQ-UI-03) can render a
	// node's Contract from the execution record alone, without re-reading the
	// original *.worker.yaml file. Same opaque-provenance treatment: a caller
	// (the registry, via Workers(wf)) computes it; nil is byte-identical to a run
	// that pinned nothing.
	Workers map[string]domain.Worker
	// Inputs supplies values for the Workflow's declared InputDecls (REQ-INPUT-01)
	// — from a CLI --input flag, an HTTP run request body, or the SDK. A value
	// here wins over a declared Default; a Required declaration satisfied by
	// neither fails the run before any node dispatches (resolveWorkflowInputs).
	// nil is byte-identical to a run that supplied nothing, for a Workflow with
	// no declared Inputs.
	Inputs map[string]string
	// ConnectionRefs records the non-secret connection resolutions a caller made
	// before dispatch (connection id -> reference metadata). The engine treats
	// it as opaque provenance and never interprets or resolves it (REQ-CONN-06).
	ConnectionRefs map[string]ConnectionRef
	// AllowUnattendedMutations is an explicit run-level opt-in. When false,
	// mutating tool calls pause before ToolCalled until a matching approval event
	// is recorded (REQ-RUNTIME-07).
	AllowUnattendedMutations bool
}

// ConnectionRef is snapshot provenance for a resolved Connection. It contains
// only labels, ids, endpoint references, and env-var names — never secret
// values (PRIN-10).
type ConnectionRef struct {
	ID        string            `json:"id"`
	Label     string            `json:"label,omitempty"`
	Kind      string            `json:"kind"`
	Type      string            `json:"type"`
	BaseURL   string            `json:"baseUrl,omitempty"`
	SecretEnv string            `json:"secretEnv,omitempty"`
	Defaults  map[string]string `json:"defaults,omitempty"`
}

// Scheduler runs workflows against a NodeExecutor, persisting artifacts and
// events. It is reusable across executions and safe to share.
type Scheduler struct {
	exec  NodeExecutor
	store *store.Store
	log   *eventlog.Log
	cache *cache.Cache
	now   func() time.Time
}

// New builds a Scheduler over the given executor, artifact store, event log, and
// node cache. cache may be nil to disable caching entirely (equivalent to every
// run using CacheOff).
func New(exec NodeExecutor, st *store.Store, log *eventlog.Log, c *cache.Cache) *Scheduler {
	return &Scheduler{exec: exec, store: st, log: log, cache: c, now: time.Now}
}

// normalizeCacheMode maps the zero value to the default (on) and, when no cache
// is wired, forces off.
func (s *Scheduler) normalizeCacheMode(m CacheMode) CacheMode {
	if s.cache == nil {
		return CacheOff
	}
	if m == "" {
		return CacheOn
	}
	return m
}

// Run executes wf from scratch.
func (s *Scheduler) Run(ctx context.Context, wf *domain.Workflow, opts RunOptions) (*Result, error) {
	return s.run(ctx, wf, opts, nil, true)
}

// emit appends one event to the log (best-effort: a log write failure must not
// crash a run, and the run's correctness does not depend on this call's return).
func (s *Scheduler) emit(execID string, t domain.EventType, nodeID string, payload map[string]any) {
	_ = s.log.Append(execID, domain.Event{
		Type:        t,
		Timestamp:   s.now(),
		ExecutionID: execID,
		NodeID:      nodeID,
		Payload:     payload,
	})
}

// nodeOutput is a succeeded node's stored output, held for the run's duration so
// downstream nodes and conditional edges can read it without a store round-trip.
type nodeOutput struct {
	Hash    string
	Content []byte
	Type    domain.ArtifactType
}

type task struct {
	node   domain.Node
	inputs []NodeInput
	attrTo string // graph node this result is attributed to (differs only for fallback)
}

type completion struct {
	id   string
	node domain.Node
	res  NodeResult
	err  error
}

// run is the shared engine for Run and Resume. precompleted seeds nodes already
// finished on a prior run (empty for a fresh run); fresh controls whether the
// snapshot and ExecutionStarted event are written.
func (s *Scheduler) run(parent context.Context, wf *domain.Workflow, opts RunOptions, precompleted map[string]nodeOutput, fresh bool) (*Result, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.RetryBackoffMax <= 0 {
		opts.RetryBackoffMax = 30 * time.Second
	}
	execID := opts.ExecutionID

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// --- Build graph structures. ---
	nodesByID := make(map[string]domain.Node, len(wf.Nodes))
	children := make(map[string][]string)
	parents := make(map[string][]domain.Edge)
	pendingParents := make(map[string]int)
	for _, n := range wf.Nodes {
		nodesByID[n.ID] = n
		pendingParents[n.ID] = 0
	}
	for _, e := range wf.Edges {
		if _, ok := nodesByID[e.From]; !ok {
			continue
		}
		if _, ok := nodesByID[e.To]; !ok {
			continue
		}
		children[e.From] = append(children[e.From], e.To)
		parents[e.To] = append(parents[e.To], e)
		pendingParents[e.To]++
	}

	// Nodes named only as a fallback target run solely when their principal
	// fails; they must not be auto-scheduled as roots. (They are assumed
	// dedicated — not otherwise wired into the graph.)
	fallbackOnly := make(map[string]bool)
	for _, n := range wf.Nodes {
		if n.OnFailure != nil && n.OnFailure.Mode == domain.FailFallback && n.OnFailure.FallbackNode != "" {
			fallbackOnly[n.OnFailure.FallbackNode] = true
		}
	}

	// --- Run state. ---
	state := make(map[string]NodeState, len(wf.Nodes))
	output := make(map[string]nodeOutput, len(wf.Nodes))
	outcomes := make(map[string]NodeOutcome, len(wf.Nodes))
	inputsUsed := make(map[string][]NodeInput)
	fbTried := make(map[string]bool)
	for _, n := range wf.Nodes {
		if res, ok := precompleted[n.ID]; ok {
			state[n.ID] = StateSucceeded
			output[n.ID] = res
			outcomes[n.ID] = NodeOutcome{State: StateSucceeded, Hash: res.Hash, Type: res.Type}
		} else {
			state[n.ID] = StatePending
		}
	}
	// Account for precompleted parents so their children can become ready.
	for id := range precompleted {
		for _, ch := range children[id] {
			pendingParents[ch]--
		}
	}

	budget := newBudgetTracker(opts.Budget, s.now)
	backoff := exponentialBackoff(opts.RetryBackoff, opts.RetryBackoffMax)
	cacheMode := s.normalizeCacheMode(opts.Cache)
	approvals := s.approvalRecords(execID)

	// Fail before any node dispatches if a required input has no value and no
	// default (PRIN-05: before the call, not after) — checked on every run,
	// fresh or resumed, since a resumed run's remaining nodes need it too.
	wfInputs, err := resolveWorkflowInputs(wf.Inputs, opts.Inputs)
	if err != nil {
		err = diagnostic.Wrap(err, diagnostic.KindValidation, "workflow_input_missing", "", "inputs.resolve", "required workflow input is missing", "provide the input value or define a default")
		return &Result{ExecutionID: execID, State: domain.ExecutionFailed, Nodes: outcomes}, err
	}

	if fresh {
		if err := s.log.WriteSnapshot(execID, Snapshot{Workflow: *wf, Budget: opts.Budget, Concurrency: opts.Concurrency, DefinitionHashes: opts.DefinitionHashes, Workers: opts.Workers, Inputs: wfInputs, ConnectionRefs: opts.ConnectionRefs, AllowUnattendedMutations: opts.AllowUnattendedMutations}); err != nil {
			return &Result{ExecutionID: execID, State: domain.ExecutionFailed, Nodes: outcomes}, fmt.Errorf("engine: write snapshot: %w", err)
		}
		s.emit(execID, domain.ExecutionStarted, "", map[string]any{"workflow": wf.ID, "version": wf.Version})
	}

	// --- Coordinator state (single-goroutine; no locks needed here). ---
	var (
		ready    []task
		toDecide []string
		inFlight int
		halted   bool
		haltErr  error
	)
	halt := func(err error) {
		if !halted {
			halted = true
			haltErr = err
			cancel()
		}
	}
	// cancelHalt finalizes a parent-cancelled run deterministically: it emits the
	// single Cancelled event and halts with the cancellation cause. It is
	// idempotent (halt is), so whichever path first observes the cancellation —
	// the ctxDone branch, or an in-flight node returning its own cancellation
	// error first under load — produces the same Cancelled outcome and exactly
	// one event. Without this, a completion processed before ctxDone could exit
	// the loop with the run mislabeled "failed".
	cancelHalt := func() {
		if halted {
			return
		}
		s.emit(execID, domain.Cancelled, "", nil)
		err := parent.Err()
		if err == nil {
			err = ErrCancelled
		}
		halt(err)
	}
	settle := func(id string, st NodeState) {
		state[id] = st
		for _, ch := range children[id] {
			pendingParents[ch]--
			if pendingParents[ch] == 0 {
				toDecide = append(toDecide, ch)
			}
		}
	}
	processDecisions := func() {
		for len(toDecide) > 0 {
			id := toDecide[0]
			toDecide = toDecide[1:]
			if state[id] != StatePending {
				continue
			}
			active := true
			var inputs []NodeInput
			for _, e := range parents[id] {
				if state[e.From] != StateSucceeded {
					active = false
					continue
				}
				ok, err := evalCondition(e.Condition, output[e.From].Content)
				if err != nil {
					err = fmt.Errorf("engine: edge %s->%s: %w", e.From, e.To, err)
					halt(diagnostic.Wrap(err, diagnostic.KindValidation, "edge_condition_invalid", id, "edge.condition", "conditional edge could not be evaluated", "check the condition path and expected upstream artifact shape"))
					active = false
					continue
				}
				if !ok {
					active = false
					continue
				}
				po := output[e.From]
				inputs = append(inputs, NodeInput{FromNode: e.From, Content: po.Content, Type: po.Type, Hash: po.Hash})
			}
			if active {
				state[id] = StateRunning
				inputsUsed[id] = inputs
				ready = append(ready, task{node: nodesByID[id], inputs: inputs, attrTo: id})
			} else {
				outcomes[id] = NodeOutcome{State: StateSkipped}
				settle(id, StateSkipped)
			}
		}
	}

	// Seed: nodes whose parents are all already terminal (roots, or all-precompleted),
	// excluding fallback-only nodes (they run only when their principal fails).
	for _, n := range wf.Nodes {
		if state[n.ID] == StatePending && pendingParents[n.ID] == 0 && !fallbackOnly[n.ID] {
			toDecide = append(toDecide, n.ID)
		}
	}

	// --- Worker pool. ---
	tasks := make(chan task)
	completions := make(chan completion)
	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				res, err := s.executeNode(ctx, execID, t.node, t.attrTo, t.inputs, opts.Budget.MaxRetriesPerNode, backoff, cacheMode, wfInputs, opts.ConnectionRefs, approvals, opts.AllowUnattendedMutations)
				completions <- completion{id: t.attrTo, node: t.node, res: res, err: err}
			}
		}()
	}

	handleCompletion := func(c completion) {
		inFlight--
		if c.err != nil {
			if errors.Is(c.err, ErrApprovalRequired) {
				outcomes[c.id] = NodeOutcome{State: StatePending, Err: c.err.Error()}
				state[c.id] = StatePending
				halt(c.err)
				return
			}
			if errors.Is(c.err, ErrApprovalRejected) {
				outcomes[c.id] = NodeOutcome{State: StateFailed, Err: c.err.Error()}
				settle(c.id, StateFailed)
				halt(c.err)
				return
			}
			// Collateral cancellation: if the run is already halting or the
			// parent context was cancelled, an in-flight node returning early is
			// a consequence of the teardown, not a node failure — record it but
			// don't override the real halt cause (cancellation / budget).
			if halted || parent.Err() != nil || errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded) {
				outcomes[c.id] = NodeOutcome{State: StateFailed, Err: c.err.Error()}
				settle(c.id, StateFailed)
				// A cancelled parent must finalize as Cancelled even if this
				// completion is observed before the ctxDone branch runs.
				if parent.Err() != nil {
					cancelHalt()
				}
				return
			}
			pol := failurePolicyOf(c.node)
			if pol.Mode == domain.FailFallback {
				if fb, ok := nodesByID[pol.FallbackNode]; ok && !fbTried[c.id] {
					fbTried[c.id] = true
					ready = append(ready, task{node: fb, inputs: inputsUsed[c.id], attrTo: c.id})
					return
				}
			}
			outcomes[c.id] = NodeOutcome{State: StateFailed, Err: c.err.Error()}
			settle(c.id, StateFailed)
			if pol.Mode != domain.FailContinue {
				halt(fmt.Errorf("%w: %q: %v", ErrNodeFailed, c.id, c.err))
			}
			return
		}
		output[c.id] = nodeOutput{Hash: c.res.Hash, Content: c.res.Content, Type: c.res.Type}
		outcomes[c.id] = NodeOutcome{State: StateSucceeded, Hash: c.res.Hash, Type: c.res.Type, CostUSD: c.res.CostUSD, Tokens: c.res.Tokens}
		budget.add(c.res.CostUSD, c.res.Tokens)
		settle(c.id, StateSucceeded)
		if budget.exceeded() {
			payload := budget.status()
			err := diagnostic.Wrap(ErrBudgetExceeded, diagnostic.KindBudget, "budget_exceeded", c.id, "budget.check", "execution exceeded its configured budget", "raise the budget or reduce workflow cost, tokens, or duration")
			payload["diagnostic"] = diagnostic.Payload(err, c.id)
			s.emit(execID, domain.BudgetExceeded, "", payload)
			halt(err)
		} else if budget.shouldWarn() {
			s.emit(execID, domain.BudgetWarning, "", budget.status())
		}
	}

	// --- Coordinator loop. ---
	processDecisions()
	ctxDone := ctx.Done()
	for inFlight > 0 || (len(ready) > 0 && !halted) {
		var taskCh chan task
		var next task
		if len(ready) > 0 && !halted {
			taskCh, next = tasks, ready[0]
		}
		select {
		case taskCh <- next:
			ready = ready[1:]
			inFlight++
		case c := <-completions:
			handleCompletion(c)
			processDecisions()
		case <-ctxDone:
			ctxDone = nil
			cancelHalt()
		}
	}
	close(tasks)
	wg.Wait()

	// --- Finalize. ---
	// A fallback node that was never triggered stays pending; it simply wasn't
	// needed, so it settles as skipped rather than counting as unresolved.
	for id := range fallbackOnly {
		if state[id] == StatePending {
			state[id] = StateSkipped
			outcomes[id] = NodeOutcome{State: StateSkipped}
		}
	}
	for _, n := range wf.Nodes {
		if _, ok := outcomes[n.ID]; !ok {
			outcomes[n.ID] = NodeOutcome{State: state[n.ID]}
		}
	}
	result := &Result{
		ExecutionID:  execID,
		Nodes:        outcomes,
		SpentCostUSD: budget.cost,
		SpentTokens:  budget.tokens,
	}
	switch {
	case errors.Is(haltErr, ErrApprovalRequired):
		result.State = domain.ExecutionPaused
	case errors.Is(haltErr, ErrBudgetExceeded):
		result.State, result.BudgetExceeded = domain.ExecutionFailed, true
	case errors.Is(haltErr, context.Canceled), errors.Is(haltErr, context.DeadlineExceeded), errors.Is(haltErr, ErrCancelled):
		result.State = domain.ExecutionCancelled
	case haltErr != nil:
		result.State = domain.ExecutionFailed
	default:
		// Ran to completion without halting. Still "failed" if any node ended
		// failed (e.g. under a continue policy) or never resolved.
		result.State = domain.ExecutionSucceeded
		for id, st := range state {
			switch st {
			case StatePending:
				result.State = domain.ExecutionFailed
				haltErr = diagnostic.Wrap(fmt.Errorf("%w: %q", ErrIncompleteGraph, id), diagnostic.KindEngine, "incomplete_graph", id, "graph.resolve", "workflow graph did not fully resolve", "check for cycles, unreachable nodes, or invalid conditional edges")
			case StateFailed:
				result.State = domain.ExecutionFailed
			}
		}
	}
	s.emit(execID, domain.ExecutionFinished, "", map[string]any{"state": string(result.State)})
	return result, haltErr
}
