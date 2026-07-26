package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// newInspectCmd implements `wee inspect <executionId>` (REQ-CLI-01): a per-node
// overview of a recorded execution, or — with --node — one node's full detail,
// including its artifact content. It reads the record only (no re-run).
func newInspectCmd() *cobra.Command {
	var (
		node      string
		workspace string
	)
	cmd := &cobra.Command{
		Use:   "inspect <executionId>",
		Short: "Inspect a recorded execution's nodes, costs, and artifacts",
		Long: "Inspect reconstructs an execution from its record and lists each node's\n" +
			"state, cost, tokens, and artifact hash. With --node <id> it drills into one\n" +
			"node: its duration (from the event timestamps) and the full artifact content.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			aud := replay.NewAuditor(eventlog.New(workspace), store.New(workspace))
			tl, err := aud.Audit(args[0])
			if err != nil {
				return coded(ExitValidation, err)
			}
			if node != "" {
				return inspectNode(cmd, tl, node)
			}
			printTimeline(cmd.OutOrStdout(), tl)
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "drill into one node's detail and artifact content")
	cmd.Flags().StringVar(&workspace, "workspace", workspaceDir, "workspace state directory")
	return cmd
}

func inspectNode(cmd *cobra.Command, tl replay.Timeline, nodeID string) error {
	rec, ok := tl.Nodes[nodeID]
	if !ok {
		return coded(ExitValidation, fmt.Errorf("no node %q in execution %s", nodeID, tl.ExecutionID))
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "node %s  (%s)\n", nodeID, tl.ExecutionID)
	fmt.Fprintf(out, "  state:    %s\n", rec.State)
	fmt.Fprintf(out, "  cost:     $%.4f\n", rec.CostUSD)
	fmt.Fprintf(out, "  tokens:   %d\n", rec.Tokens)
	fmt.Fprintf(out, "  type:     %s\n", rec.Type)
	fmt.Fprintf(out, "  hash:     %s\n", rec.Hash)
	if d, ok := nodeDuration(tl.Events, nodeID); ok {
		fmt.Fprintf(out, "  duration: %s\n", d)
	}
	if rec.Err != "" {
		fmt.Fprintf(out, "  error:    %s\n", rec.Err)
	}
	if len(rec.Content) > 0 {
		fmt.Fprintf(out, "  artifact:\n%s\n", rec.Content)
	}
	return nil
}

// nodeDuration is the span from a node's WorkerStarted to its WorkerFinished, if
// both were recorded — a best-effort wall-clock read from the event timestamps.
func nodeDuration(events []domain.Event, nodeID string) (time.Duration, bool) {
	var start, finish time.Time
	for _, ev := range events {
		if ev.NodeID != nodeID {
			continue
		}
		switch ev.Type {
		case domain.WorkerStarted:
			start = ev.Timestamp
		case domain.WorkerFinished:
			finish = ev.Timestamp
		}
	}
	if start.IsZero() || finish.IsZero() {
		return 0, false
	}
	return finish.Sub(start), true
}
