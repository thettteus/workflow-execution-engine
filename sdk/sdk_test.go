package sdk_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/sdk"
)

func reviewer(id string) domain.Worker {
	return domain.Worker{
		ID: id, Version: "1.0.0",
		Objective:     "review the diff",
		Constraints:   []string{"cite a line"},
		Tools:         []string{},
		ContextPolicy: domain.ContextPolicy{Mode: "diff-only"},
		Contract: domain.Contract{
			Goal: "verdict", Rules: []string{"be terse"}, SuccessCriteria: []string{"no defect missed"},
			MaxRetries: 1, OutputSchema: map[string]any{"type": "object"},
		},
		Model: domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}
}

// yamlEquivalent is the same graph the SDK builds below, written by hand. Node
// and edge order match the builder's emission order — content hashing preserves
// array order (only object keys are sorted), so the orders must agree.
const yamlEquivalent = `id: pr-review
version: 1.0.0
nodes:
  - id: review-security
    worker: review-security@1.0.0
  - id: review-style
    worker: review-style@1.0.0
  - id: fix
    worker: fixer@1.0.0
  - id: test
    tool:
      toolName: terminal
      input:
        command: go
        args: ["test", "./..."]
edges:
  - { from: review-security, to: fix }
  - { from: review-style, to: fix }
  - { from: fix, to: test }
budget:
  maxCostUsd: 0.5
  maxTokens: 20000
  maxDurationMs: 120000
  maxRetriesPerNode: 2
`

// TestSDKAndYAMLHashIdentical is the REQ-SDK-01 acceptance test: a workflow
// authored via the SDK and the same workflow written in YAML produce
// byte-identical content hashes (REQ-DEF-02). The SDK is not a privileged path.
func TestSDKAndYAMLHashIdentical(t *testing.T) {
	fixer := reviewer("fixer")
	built, err := sdk.New("pr-review", "1.0.0").
		Budget(domain.Budget{MaxCostUSD: 0.5, MaxTokens: 20000, MaxDurationMs: 120000, MaxRetriesPerNode: 2}).
		Parallel(
			sdk.Worker("review-security", reviewer("review-security")),
			sdk.Worker("review-style", reviewer("review-style")),
		).
		Merge("fix", fixer).
		Tool("test", domain.ToolCall{ToolName: "terminal", Input: map[string]any{"command": "go", "args": []any{"test", "./..."}}}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var fromYAML domain.Workflow
	if err := serialize.UnmarshalYAML([]byte(yamlEquivalent), &fromYAML); err != nil {
		t.Fatalf("decode YAML: %v", err)
	}

	sdkHash, err := canonical.Hash(built.Definition())
	if err != nil {
		t.Fatalf("hash SDK workflow: %v", err)
	}
	yamlHash, err := canonical.Hash(fromYAML)
	if err != nil {
		t.Fatalf("hash YAML workflow: %v", err)
	}
	if sdkHash != yamlHash {
		t.Errorf("content hashes differ:\n  SDK:  %s\n  YAML: %s\n\nSDK def:  %+v\nYAML def: %+v", sdkHash, yamlHash, built.Definition(), fromYAML)
	}
}

// TestBuildRejectsDuplicateNodeID: the builder surfaces a duplicate graph-node
// id at Build rather than emitting an invalid workflow.
func TestBuildRejectsDuplicateNodeID(t *testing.T) {
	_, err := sdk.New("wf", "1.0.0").
		Worker("a", reviewer("wa")).
		Worker("a", reviewer("wb")).
		Build()
	if err == nil {
		t.Error("expected a duplicate-node-id error")
	}
}

// TestParallelMergeGraphShape checks the frontier wiring: Parallel fans out from
// the frontier, Merge fans back in.
func TestParallelMergeGraphShape(t *testing.T) {
	wf, err := sdk.New("wf", "1.0.0").
		Parallel(sdk.Worker("a", reviewer("wa")), sdk.Worker("b", reviewer("wb"))).
		Merge("m", reviewer("wm")).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	def := wf.Definition()
	if len(def.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(def.Nodes))
	}
	// a and b are roots (no parents); m depends on both.
	var toM int
	for _, e := range def.Edges {
		if e.To == "m" {
			toM++
		}
		if e.From == "a" && e.To == "b" || e.From == "b" && e.To == "a" {
			t.Errorf("parallel branches must not depend on each other: %+v", e)
		}
	}
	if toM != 2 {
		t.Errorf("merge node should fan in from both branches, got %d edges into m", toM)
	}
}

// TestDescribeWorkerPreservesDescriptionInSDKDefinition is REQ-WORKER-08's
// Go authoring surface: SDK-authored Workers carry the same canonical optional
// description as YAML-authored Workers.
func TestDescribeWorkerPreservesDescriptionInSDKDefinition(t *testing.T) {
	w := sdk.DescribeWorker(reviewer("reviewer"), "Reviews a change for correctness.")
	if w.Description != "Reviews a change for correctness." {
		t.Fatalf("Description = %q", w.Description)
	}
	built, err := sdk.New("wf", "1.0.0").Worker("review", w).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Definition().Nodes[0].Worker != "reviewer@1.0.0" {
		t.Fatalf("worker ref = %q", built.Definition().Nodes[0].Worker)
	}
}
