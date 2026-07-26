package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/model/providers"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// RunOptions configures a programmatic run. Every field has a sensible zero
// value, so `Run(ctx, sdk.RunOptions{})` works for a worker-only workflow with
// a provider key in the environment.
type RunOptions struct {
	// Workspace is the state directory (executions, artifacts, cache). Default
	// ".workflow".
	Workspace string
	// Providers resolves a Worker's provider name to an implementation. Default
	// providers.Default() (openai + anthropic, keys from the environment).
	Providers *model.Registry
	// Tools is the sandboxed tool registry for tool-backed nodes. Default empty
	// (a worker-only workflow needs none); wire the built-ins yourself for tools.
	Tools *tool.Registry
	// Concurrency caps parallel node execution (0 = engine default).
	Concurrency int
	// Cache selects the node cache mode ("" = on).
	Cache engine.CacheMode
	// Inputs supplies values for the Workflow's declared Inputs (REQ-INPUT-01) —
	// same mechanism `wee run --input` and the HTTP API use.
	Inputs map[string]string
	// AllowMutationsWithoutApproval is the explicit programmatic opt-in for
	// unattended mutating tool calls (REQ-RUNTIME-07). The zero value pauses
	// before mutation until an approval event exists.
	AllowMutationsWithoutApproval bool
}

// Run assembles the engine over the workflow's in-code Workers and starts it,
// returning immediately with an Execution. Events stream on Execution.Events();
// Execution.Wait() blocks for the final result. The workflow's own Budget
// applies; definition hashes are pinned in the snapshot (REQ-VERSION-02).
func (w *Workflow) Run(ctx context.Context, opts RunOptions) (*Execution, error) {
	baseDir := opts.Workspace
	if baseDir == "" {
		baseDir = ".workflow"
	}
	provReg := opts.Providers
	if provReg == nil {
		provReg = providers.Default()
	}
	tools := opts.Tools
	if tools == nil {
		tools = tool.NewRegistry()
	}

	reg := registry.New()
	if err := reg.RegisterWorkflow(w.def); err != nil {
		return nil, fmt.Errorf("sdk: register workflow: %w", err)
	}
	for _, wk := range w.workers {
		if err := reg.RegisterWorker(wk); err != nil {
			return nil, fmt.Errorf("sdk: register worker: %w", err)
		}
	}

	dispatch := engine.NewDispatchExecutor(
		engine.NewWorkerExecutor(reg, provReg),
		engine.NewToolExecutor(tools),
	)
	log := eventlog.New(baseDir)
	st := store.New(baseDir)
	sched := engine.New(dispatch, st, log, cache.New(baseDir))

	execID := newExecutionID(w.def.ID)
	runOpts := engine.RunOptions{
		ExecutionID:              execID,
		Concurrency:              opts.Concurrency,
		Budget:                   w.def.Budget,
		Cache:                    opts.Cache,
		DefinitionHashes:         reg.DefinitionHashes(w.def),
		Inputs:                   opts.Inputs,
		AllowUnattendedMutations: opts.AllowMutationsWithoutApproval,
	}

	e := &Execution{
		ID:     execID,
		events: make(chan domain.Event, 256),
		done:   make(chan struct{}),
		store:  st,
		def:    w.def,
	}
	go func() {
		res, err := sched.Run(ctx, &w.def, runOpts)
		e.mu.Lock()
		e.result, e.runErr = res, err
		e.mu.Unlock()
		close(e.done)
	}()
	go e.stream(log)
	return e, nil
}

// Execution is a started run. Consume Events() for the live stream and/or call
// Wait() for the final result. The two are independent: Wait() returns even if
// Events() is never read.
type Execution struct {
	ID     string
	events chan domain.Event
	done   chan struct{}
	store  *store.Store
	def    domain.Workflow

	mu     sync.Mutex
	result *engine.Result
	runErr error
}

// Events returns the live event stream, closed when the run finishes. It is a
// best-effort convenience view: if the consumer falls behind the buffer, events
// are dropped from this channel — the event log remains the complete, ordered
// record (PRIN-02), readable via core/replay.
func (e *Execution) Events() <-chan domain.Event { return e.events }

// Wait blocks until the run finishes and returns its result and error (the same
// pair engine.Scheduler.Run returns).
func (e *Execution) Wait() (*engine.Result, error) {
	<-e.done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result, e.runErr
}

// stream polls the event log and forwards new events to the channel until the
// run finishes, then drains the rest and closes. Sends are non-blocking so a
// slow or absent consumer never stalls the run or leaks this goroutine.
func (e *Execution) stream(log *eventlog.Log) {
	defer close(e.events)
	emitted := 0
	forward := func() {
		events, err := log.ReadAll(e.ID)
		if err != nil {
			return
		}
		for ; emitted < len(events); emitted++ {
			select {
			case e.events <- events[emitted]:
			default: // consumer behind; event stays in the log
			}
		}
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-e.done:
			forward() // final drain
			return
		case <-tick.C:
			forward()
		}
	}
}

// newExecutionID mints a sortable execution id (workflow id + UTC timestamp +
// short random suffix).
func newExecutionID(workflowID string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%s", workflowID, time.Now().UTC().Format("20060102T150405"), hex.EncodeToString(b[:]))
}
