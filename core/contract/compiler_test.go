package contract_test

import (
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/contract"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/policy"
)

// TestCompiledContextIsDiffOnly is REQ-CTXPOL-01: a Worker configured with a
// diff-only context policy compiles a model call containing only the diff
// artifact — nothing from a sibling Planning node's output.
func TestCompiledContextIsDiffOnly(t *testing.T) {
	reviewer := domain.Worker{
		ID: "reviewer", Version: "1.0.0", Objective: "review the change",
		ContextPolicy: domain.ContextPolicy{Mode: domain.ContextDiffOnly},
		Contract:      domain.Contract{OutputSchema: map[string]any{"type": "object"}},
	}

	available := []policy.Item{
		{FromNode: "planner", Type: domain.ArtifactMarkdown, Hash: "h-plan", Content: []byte("SECRET PLAN: rewrite everything")},
		{FromNode: "differ", Type: domain.ArtifactDiff, Hash: "h-diff", Content: []byte("@@ -1 +1 @@ the diff")},
	}

	admitted, err := policy.Resolve(reviewer.ContextPolicy, available)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	messages := contract.Compile(reviewer, admitted, "")

	var body strings.Builder
	for _, m := range messages {
		body.WriteString(string(m.Role))
		body.WriteString(": ")
		body.WriteString(m.Content)
		body.WriteString("\n")
	}
	compiled := body.String()

	if !strings.Contains(compiled, "the diff") {
		t.Errorf("compiled context should include the diff artifact:\n%s", compiled)
	}
	if strings.Contains(compiled, "SECRET PLAN") {
		t.Errorf("compiled context leaked the sibling Planning node's output:\n%s", compiled)
	}
}

// TestCompileSystemStatesSchema confirms the required output schema is stated in
// the system message (the single place model-input text is built).
func TestCompileSystemStatesSchema(t *testing.T) {
	w := domain.Worker{
		ID: "w", Objective: "do the thing",
		Contract: domain.Contract{OutputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"score": map[string]any{"type": "number"}},
		}},
	}
	msgs := contract.Compile(w, nil, "")
	if len(msgs) == 0 || msgs[0].Role != model.RoleSystem {
		t.Fatalf("first message should be the system message, got %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "score") || !strings.Contains(msgs[0].Content, "JSON Schema") {
		t.Errorf("system message should state the output schema:\n%s", msgs[0].Content)
	}
}

// TestCompileExcludesWorkerDescription is REQ-WORKER-08: description is
// human-facing definition metadata. It is canonical and versioned, but must
// never enter the model input or alter model behavior.
func TestCompileExcludesWorkerDescription(t *testing.T) {
	const description = "Human-only reviewer description that must stay out of model input."
	w := domain.Worker{
		ID: "reviewer", Version: "1.0.0", Description: description,
		Objective: "review the change",
		Contract:  domain.Contract{OutputSchema: map[string]any{"type": "object"}},
	}

	for _, msg := range contract.Compile(w, nil, "") {
		if strings.Contains(msg.Content, description) {
			t.Fatalf("compiled model input contains Worker.description (REQ-WORKER-08): %q", msg.Content)
		}
	}
}
