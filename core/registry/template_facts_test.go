package registry_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/registry"
)

func toolNode(id, toolName string, input map[string]any) domain.Node {
	return domain.Node{ID: id, Tool: &domain.ToolCall{ToolName: toolName, Input: input}}
}

// TestDeriveTemplateFactsWriteCapable covers every safe/unsafe literal per
// tool, plus the case the allowlist polarity exists for: an unresolved
// "${...}" placeholder must default to write-capable, never read-only,
// because it can only be resolved at run time (core/engine/tool_input.go).
func TestDeriveTemplateFactsWriteCapable(t *testing.T) {
	cases := []struct {
		name string
		node domain.Node
		want bool
	}{
		{"git status is safe", toolNode("n", "git", map[string]any{"op": "status"}), false},
		{"git diff is safe", toolNode("n", "git", map[string]any{"op": "diff"}), false},
		{"git add is unsafe", toolNode("n", "git", map[string]any{"op": "add"}), true},
		{"git commit is unsafe", toolNode("n", "git", map[string]any{"op": "commit"}), true},
		{"git branch is unsafe", toolNode("n", "git", map[string]any{"op": "branch"}), true},
		{"filesystem read is safe", toolNode("n", "filesystem", map[string]any{"op": "read"}), false},
		{"filesystem list is safe", toolNode("n", "filesystem", map[string]any{"op": "list"}), false},
		{"filesystem write is unsafe", toolNode("n", "filesystem", map[string]any{"op": "write"}), true},
		{"http GET is safe", toolNode("n", "http", map[string]any{"method": "GET"}), false},
		{"http POST is unsafe", toolNode("n", "http", map[string]any{"method": "POST"}), true},
		{"terminal is always unsafe", toolNode("n", "terminal", map[string]any{"command": "go"}), true},
		{"unknown tool is unsafe", toolNode("n", "made-up-tool", map[string]any{}), true},
		{
			"unresolved git op placeholder defaults to unsafe, not safe",
			toolNode("n", "git", map[string]any{"op": "${input:mode}"}),
			true,
		},
		{
			"unresolved http method placeholder defaults to unsafe, not safe",
			toolNode("n", "http", map[string]any{"method": "${input:verb}"}),
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wf := domain.Workflow{ID: "wf", Version: "1.0.0", Nodes: []domain.Node{c.node}}
			got := registry.DeriveTemplateFacts(wf)
			if got.WriteCapable != c.want {
				t.Errorf("WriteCapable = %v, want %v", got.WriteCapable, c.want)
			}
		})
	}
}

// TestDeriveTemplateFactsAggregatesAcrossNodes covers Tools dedup/order, the
// budget-derived cost/duration fields, and Inputs mirroring, on a multi-node
// workflow — the shape a real template actually has.
func TestDeriveTemplateFactsAggregatesAcrossNodes(t *testing.T) {
	wf := domain.Workflow{
		ID:      "wf",
		Version: "1.0.0",
		Nodes: []domain.Node{
			toolNode("fetch", "http", map[string]any{"method": "GET"}),
			{ID: "review", Worker: "reviewer@1.0.0"}, // worker-backed, no tool
			toolNode("fetch2", "http", map[string]any{"method": "GET"}),
		},
		Inputs: []domain.InputDecl{
			{Name: "prUrl", Required: true, Description: "PR diff URL"},
			{Name: "note", Default: "n/a"},
		},
		Budget: domain.Budget{MaxCostUSD: 0.03, MaxDurationMs: 90000},
	}

	got := registry.DeriveTemplateFacts(wf)

	if got.WriteCapable {
		t.Error("WriteCapable = true, want false for an all-GET/worker-only workflow")
	}
	if len(got.Tools) != 1 || got.Tools[0] != "http" {
		t.Errorf("Tools = %v, want deduplicated [http]", got.Tools)
	}
	if got.ExpectedCostUsd != 0.03 || got.ExpectedDurationMs != 90000 {
		t.Errorf("cost/duration = %v/%v, want 0.03/90000", got.ExpectedCostUsd, got.ExpectedDurationMs)
	}
	if len(got.Inputs) != 2 || got.Inputs[0].Name != "prUrl" || !got.Inputs[0].Required {
		t.Errorf("Inputs = %+v, want [{prUrl required=true ...} {note ...}]", got.Inputs)
	}
}

// TestDeriveTemplateFactsToolsAndInputsNeverMarshalNull covers a real
// regression: a nil Go slice marshals to JSON `null`, not `[]`. A client
// typed against a non-nullable array (ui/src/core/audit.ts's Template)
// crashes calling .length on `null` — caught live when refactor-plan (no
// declared workflow inputs) round-tripped through a real GET /api/templates
// as "inputs": null before DeriveTemplateFacts initialized these non-nil.
func TestDeriveTemplateFactsToolsAndInputsNeverMarshalNull(t *testing.T) {
	wf := domain.Workflow{ID: "wf", Version: "1.0.0", Nodes: []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}}}
	facts := registry.DeriveTemplateFacts(wf)

	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(raw)
	if want := `"tools":[]`; !strings.Contains(got, want) {
		t.Errorf("marshaled JSON = %s, want it to contain %s (not null)", got, want)
	}
	if want := `"inputs":[]`; !strings.Contains(got, want) {
		t.Errorf("marshaled JSON = %s, want it to contain %s (not null)", got, want)
	}
}

// TestDeriveTemplateFactsToolBackedNodeIsNeverWriteCapable covers a node with
// no Tool at all (Worker-backed or malformed) — it must never contribute to
// WriteCapable or Tools.
func TestDeriveTemplateFactsToolBackedNodeIsNeverWriteCapable(t *testing.T) {
	wf := domain.Workflow{
		ID:      "wf",
		Version: "1.0.0",
		Nodes:   []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}},
	}
	got := registry.DeriveTemplateFacts(wf)
	if got.WriteCapable {
		t.Error("WriteCapable = true, want false for a workflow with only Worker-backed nodes")
	}
	if len(got.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", got.Tools)
	}
}
