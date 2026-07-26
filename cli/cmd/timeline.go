package cmd

import (
	"fmt"
	"io"

	"github.com/thettteus/workflow-execution-engine/core/replay"
)

// shortHash trims a content hash to a readable prefix for terminal listings;
// the full hash is always available in the artifact store and the event log.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "—"
	}
	return h
}

// printTimeline renders a reconstructed execution as a per-node listing in graph
// order, followed by totals. It is shared by `replay` (audit) and `inspect`.
func printTimeline(w io.Writer, tl replay.Timeline) {
	fmt.Fprintf(w, "execution %s  (%s@%s)\n", tl.ExecutionID, tl.Workflow.ID, tl.Workflow.Version)
	for _, n := range tl.Workflow.Nodes {
		rec := tl.Nodes[n.ID]
		kind := "worker:" + n.Worker
		if n.Tool != nil {
			kind = "tool:" + n.Tool.ToolName
		}
		fmt.Fprintf(w, "  %-16s %-11s $%.4f  %6d tok  %s  [%s]\n",
			n.ID, rec.State, rec.CostUSD, rec.Tokens, shortHash(rec.Hash), kind)
		if rec.Err != "" {
			fmt.Fprintf(w, "      error: %s\n", rec.Err)
		}
	}
	fmt.Fprintf(w, "total: $%.4f, %d tokens\n", tl.SpentCostUSD, tl.SpentTokens)
}
