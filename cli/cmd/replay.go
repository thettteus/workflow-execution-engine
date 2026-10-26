package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

type replayFlags struct {
	execute   bool
	workflow  string
	workspace string
}

// newReplayCmd implements `wee replay <executionId>` (REQ-CLI-01, REQ-REPLAY-03).
// Without flags it audits — reconstructs the recorded timeline at zero cost,
// touching no model or tool. With --execute it re-runs the frozen workflow
// (reusing the cache for unchanged nodes) and reports the divergence.
func newReplayCmd() *cobra.Command {
	var f replayFlags
	cmd := &cobra.Command{
		Use:   "replay <executionId>",
		Short: "Audit a recorded execution, or re-execute it and report divergence",
		Long: "Replay has two distinct modes (never conflated):\n\n" +
			"  wee replay <id>             audit — reconstruct the recorded timeline from\n" +
			"                              disk alone, zero model calls, zero cost.\n" +
			"  wee replay <id> --execute   re-execute the frozen workflow; unchanged nodes\n" +
			"                              are served from cache, only invalidated nodes\n" +
			"                              re-run. Reports which nodes were cached vs\n" +
			"                              re-executed. A re-executed LLM node's output is\n" +
			"                              NOT guaranteed identical — see docs/replay-honesty.md.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.execute {
				return runReplayExecute(cmd, args[0], f)
			}
			return runReplayAudit(cmd, args[0], f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.execute, "execute", false, "re-execute the frozen workflow instead of auditing")
	fl.StringVar(&f.workflow, "workflow", "", "with --execute: workflow file whose sibling Workers to load for any re-executed node")
	fl.StringVar(&f.workspace, "workspace", workspaceDir, "workspace state directory")
	return cmd
}

func runReplayAudit(cmd *cobra.Command, execID string, f replayFlags) error {
	log := eventlog.New(f.workspace)
	aud := replay.NewAuditor(log, store.New(f.workspace))
	tl, err := aud.Audit(execID)
	if err != nil {
		return coded(ExitValidation, err)
	}
	printTimeline(cmd.OutOrStdout(), tl)
	// An audit that reconstructs a timeline without checking it is only a
	// display: verify the hash chain (REQ-EVENT-03, ADR 0007) and fail the audit
	// on a break, after printing what was recorded so the break can be located.
	if err := log.Verify(execID); err != nil {
		var ce *eventlog.ChainError
		if errors.As(err, &ce) {
			fmt.Fprintf(cmd.OutOrStdout(), "integrity: FAILED — hash chain broken at event line %d\n", ce.Line)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "integrity: FAILED")
		}
		return coded(ExitValidation, fmt.Errorf("event log failed hash-chain verification: %w", err))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "integrity: hash chain verified")
	return nil
}

func runReplayExecute(cmd *cobra.Command, execID string, f replayFlags) error {
	if f.workflow == "" {
		return coded(ExitValidation, fmt.Errorf("--execute needs --workflow <path> to load the Workers any re-executed node requires"))
	}

	// Assemble an engine over the same workspace (so the shared cache/store make
	// unchanged nodes free) using the Workers beside --workflow.
	asm, err := runner.Load(f.workflow, f.workspace)
	if err != nil {
		return coded(ExitValidation, err)
	}

	aud := replay.NewAuditor(eventlog.New(f.workspace), store.New(f.workspace))
	original, err := aud.Audit(execID)
	if err != nil {
		return coded(ExitValidation, err)
	}

	newID := runner.NewExecutionID(original.Workflow.ID)
	reexec := replay.NewReexecuter(asm.Log, asm.Scheduler)
	if _, err := reexec.Reexecute(cmd.Context(), execID, newID); err != nil {
		return exitForRun(nil, err)
	}

	reexecuted, err := aud.Audit(newID)
	if err != nil {
		return fmt.Errorf("audit re-execution: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "re-executed %s as %s\n\n", execID, newID)
	for _, d := range replay.Divergence(original, reexecuted) {
		fmt.Fprintf(out, "  %-16s %-11s %s → %s\n", d.NodeID, d.Status, shortHash(d.OriginalHash), shortHash(d.NewHash))
	}
	fmt.Fprintf(out, "\noriginal: $%.4f  re-execution: $%.4f\n", original.SpentCostUSD, reexecuted.SpentCostUSD)
	return nil
}
