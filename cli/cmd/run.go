package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/cli/internal/render"
	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

type runFlags struct {
	json                          bool
	cache                         string
	concurrency                   int
	resume                        string
	budget                        float64
	workspace                     string
	inputs                        map[string]string
	allowMutationsWithoutApproval bool
}

// newRunCmd implements `wee run <workflow.yaml>` (REQ-CLI-01/03/04). It assembles
// the full engine, runs (or resumes) the workflow, streams events live to the
// terminal or as line-delimited JSON, and exits with the REQ-CLI-04 code.
//
// --input supplies values for a workflow's declared Inputs (REQ-INPUT-01,
// M1.14a) — the engine capability the original M1.9 gap was waiting on.
// Secrets still never go here: "${env:NAME}" (an OS environment variable) is
// the only door for anything that shouldn't be recorded in the audit trail.
func newRunCmd() *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run <workflow.yaml>",
		Short: "Run (or resume) a workflow, streaming events live",
		Long: "Run assembles the engine from the workflow file and its sibling Workers,\n" +
			"executes the graph, and streams events as they happen. With --json it emits\n" +
			"line-delimited event JSON (the same stream the UI consumes). Exit codes:\n" +
			"0 success, 1 node failure, 2 budget exceeded, 3 validation error, 130 SIGINT.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd, args[0], f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.json, "json", false, "emit line-delimited event JSON instead of live status")
	fl.StringVar(&f.cache, "cache", "on", "cache mode: on | off | readonly")
	fl.IntVar(&f.concurrency, "concurrency", 0, "max nodes to run in parallel (0 = engine default)")
	fl.StringVar(&f.resume, "resume", "", "resume a prior execution by id instead of starting fresh")
	fl.Float64Var(&f.budget, "budget", 0, "override the workflow's max cost in USD (0 = use the workflow's)")
	fl.StringVar(&f.workspace, "workspace", workspaceDir, "workspace state directory")
	fl.StringToStringVarP(&f.inputs, "input", "i", nil, "workflow input KEY=VALUE (repeatable)")
	fl.BoolVar(&f.allowMutationsWithoutApproval, "allow-mutations-without-approval", false, "explicitly allow mutating tool calls without approval checkpoints")
	return cmd
}

func runRun(cmd *cobra.Command, path string, f runFlags) error {
	cacheMode, err := parseCacheMode(f.cache)
	if err != nil {
		return coded(ExitValidation, err)
	}

	asm, err := runner.Load(path, f.workspace)
	if err != nil {
		return coded(ExitValidation, err)
	}
	// Pre-flight validation: a malformed graph exits 3, never a mid-run crash.
	if err := validateWorkflowFile(asm.Workflow, path); err != nil {
		return coded(ExitValidation, err)
	}

	// SIGINT cancels the run; the engine finalizes as cancelled and we exit 130.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	var renderer render.Renderer
	if f.json {
		renderer = render.JSON(cmd.OutOrStdout())
	} else {
		renderer = render.Human(cmd.OutOrStdout())
	}

	fresh := f.resume == ""
	execID := f.resume
	if fresh {
		execID = runner.NewExecutionID(asm.Workflow.ID)
	}

	opts := engine.RunOptions{
		ExecutionID:              execID,
		Concurrency:              f.concurrency,
		Budget:                   budgetFor(asm.Workflow, f.budget),
		Cache:                    cacheMode,
		DefinitionHashes:         asm.Registry.DefinitionHashes(*asm.Workflow),
		Workers:                  asm.Registry.Workers(*asm.Workflow),
		Inputs:                   f.inputs,
		AllowUnattendedMutations: f.allowMutationsWithoutApproval,
	}

	type outcome struct {
		res *engine.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		var res *engine.Result
		var err error
		if fresh {
			res, err = asm.Scheduler.Run(ctx, asm.Workflow, opts)
		} else {
			res, err = asm.Scheduler.Resume(ctx, execID)
		}
		done <- outcome{res, err}
	}()

	streamAndFinish := streamer(asm, execID, renderer)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case o := <-done:
			streamAndFinish(o.res, true)
			return exitForRun(o.res, o.err)
		case <-ticker.C:
			streamAndFinish(nil, false)
		}
	}
}

// streamer returns a function that drains any new events from the log into the
// renderer; when final is true it also calls Finish. Events are read from the
// log (the ordered, hash-chained source of truth), so the stream is exactly
// what an audit or the UI would see — never a second in-memory copy (PRIN-02).
func streamer(asm *runner.Assembly, execID string, r render.Renderer) func(res *engine.Result, final bool) {
	emitted := 0
	return func(res *engine.Result, final bool) {
		events, err := asm.Log.ReadAll(execID)
		if err == nil {
			for ; emitted < len(events); emitted++ {
				r.Event(events[emitted])
			}
		}
		if final {
			r.Finish(res)
		}
	}
}

// exitForRun maps an engine result to the process exit code REQ-CLI-04 defines.
func exitForRun(res *engine.Result, err error) error {
	switch {
	case errors.Is(err, engine.ErrBudgetExceeded):
		return coded(ExitBudget, err)
	case errors.Is(err, engine.ErrCancelled) || errors.Is(err, context.Canceled):
		return coded(ExitCancelled, err)
	case errors.Is(err, engine.ErrMissingInput):
		// Same class as a schema/graph validation failure: a malformed
		// invocation caught before any node dispatched, not a run outcome.
		return coded(ExitValidation, err)
	case err != nil:
		// Node failure or an unresolved graph — a generic run failure.
		return coded(ExitNodeFailure, err)
	default:
		return nil
	}
}

// parseCacheMode maps the --cache flag to an engine.CacheMode.
func parseCacheMode(s string) (engine.CacheMode, error) {
	switch s {
	case "on", "":
		return engine.CacheOn, nil
	case "off":
		return engine.CacheOff, nil
	case "readonly":
		return engine.CacheReadOnly, nil
	default:
		return "", fmt.Errorf("invalid --cache %q (want on, off, or readonly)", s)
	}
}

// budgetFor applies the optional --budget cost override to the workflow's budget.
func budgetFor(wf *domain.Workflow, overrideCostUSD float64) domain.Budget {
	b := wf.Budget
	if overrideCostUSD > 0 {
		b.MaxCostUSD = overrideCostUSD
	}
	return b
}
