// Command sdk-pr-review is the flagship demo authored via the Go SDK (REQ-SDK-03):
// three diff-scoped reviewers run in parallel, a fixer merges their findings,
// then a test run and a commit — the graph docs/VISION.md describes, in under
// 100 lines. It needs OPENAI_API_KEY to run the LLM nodes; the graph itself
// content-hashes identically to the equivalent hand-written YAML (REQ-SDK-01).
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool"
	"github.com/thettteus/workflow-execution-engine/core/tool/git"
	"github.com/thettteus/workflow-execution-engine/core/tool/terminal"
	"github.com/thettteus/workflow-execution-engine/sdk"
)

// reviewer builds a diff-scoped review Worker with a tight, bounded contract
// (anti-slop by construction, PRIN-08): at most five short findings.
func reviewer(id, focus string) domain.Worker {
	return domain.Worker{
		ID: id, Version: "1.0.0",
		Objective:     "Review the diff for " + focus + " issues.",
		Constraints:   []string{"Judge only the diff.", "Cite a line for every finding."},
		Tools:         []string{},
		ContextPolicy: domain.ContextPolicy{Mode: "diff-only"},
		Contract: domain.Contract{
			Goal:            "Report " + focus + " findings.",
			Rules:           []string{"At most five findings, most severe first."},
			SuccessCriteria: []string{"No " + focus + " defect left unreported."},
			MaxRetries:      2,
			OutputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []any{"findings"},
				"properties": map[string]any{
					"findings": map[string]any{
						"type": "array", "maxItems": 5,
						"items": map[string]any{"type": "string", "maxLength": 200},
					},
				},
			},
		},
		Model: domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}
}

func main() {
	wf, err := sdk.New("pr-review", "1.0.0").
		Budget(domain.Budget{MaxCostUSD: 0.5, MaxTokens: 40000, MaxDurationMs: 180000, MaxRetriesPerNode: 2}).
		Parallel(
			sdk.Worker("review-security", reviewer("review-security", "security")),
			sdk.Worker("review-style", reviewer("review-style", "style")),
			sdk.Worker("review-correctness", reviewer("review-correctness", "correctness")),
		).
		Merge("fix", reviewer("fixer", "fixable")).
		Tool("test", domain.ToolCall{ToolName: "terminal", Input: map[string]any{"command": "go", "args": []any{"test", "./..."}}}).
		Tool("commit", domain.ToolCall{ToolName: "git", Input: map[string]any{"op": "commit", "message": "automated review pass"}}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	tools := tool.NewRegistry()
	tools.Register(terminal.New(".", []string{"go"}, 60*time.Second, domain.ArtifactTestResult))
	tools.Register(git.New(".", 30*time.Second))

	exec, err := wf.Run(context.Background(), sdk.RunOptions{Tools: tools})
	if err != nil {
		log.Fatal(err)
	}
	for ev := range exec.Events() {
		fmt.Printf("%-18s %s\n", ev.Type, ev.NodeID)
	}
	res, err := exec.Wait()
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	fmt.Printf("\n%s — $%.4f, %d tokens\n", res.State, res.SpentCostUSD, res.SpentTokens)
}
