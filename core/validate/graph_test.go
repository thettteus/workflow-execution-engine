package validate_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/core/validate"
)

func TestGraphAcceptsValidDiamond(t *testing.T) {
	wf, err := serialize.LoadWorkflow(filepath.Join("..", "serialize", "testdata", "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.Graph(wf, nil); err != nil {
		t.Errorf("valid diamond graph rejected:\n%v", err)
	}
}

func TestGraphRejectsUnresolvedEdge(t *testing.T) {
	path := filepath.Join("testdata", "unresolved-edge.yaml")
	wf, err := serialize.LoadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := serialize.LoadSource(path)
	if err != nil {
		t.Fatal(err)
	}

	err = validate.Graph(wf, src)
	if err == nil {
		t.Fatal("expected an error for an edge pointing at a non-existent node")
	}
	var ge *validate.GraphError
	if !errors.As(err, &ge) {
		t.Fatalf("expected *validate.GraphError, got %T", err)
	}

	msg := err.Error()
	if !strings.Contains(msg, "ghost") {
		t.Errorf("error should name the unresolved node id 'ghost':\n%s", msg)
	}
	if !strings.Contains(msg, "line 10") {
		t.Errorf("error should cite the edge's source line (10):\n%s", msg)
	}
}

func TestGraphRejectsCycle(t *testing.T) {
	path := filepath.Join("testdata", "cycle.yaml")
	wf, err := serialize.LoadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.Graph(wf, nil); err == nil {
		t.Fatal("expected an error for a cyclic graph")
	} else if !strings.Contains(err.Error(), "a -> b -> c -> a") {
		t.Errorf("error should name the cycle a -> b -> c -> a:\n%v", err)
	}
}

func TestGraphRejectsOrphan(t *testing.T) {
	wf := &domain.Workflow{
		ID: "orphaned", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "a", Worker: "w@1"},
			{ID: "b", Worker: "w@1"},
			{ID: "island", Worker: "w@1"},
		},
		Edges:  []domain.Edge{{From: "a", To: "b"}},
		Budget: domain.Budget{MaxCostUSD: 1, MaxTokens: 1, MaxDurationMs: 1, MaxRetriesPerNode: 1},
	}
	err := validate.Graph(wf, nil)
	if err == nil || !strings.Contains(err.Error(), `"island" is an orphan`) {
		t.Errorf("expected an orphan error naming 'island', got: %v", err)
	}
}

// TestNodeRequiresExactlyOneOfWorkerOrTool is the REQ-WORKER-04 acceptance
// test (ADR 0008): a node declaring both a worker and a tool, or neither, is
// rejected with a clear, positional error; declaring exactly one is valid.
func TestNodeRequiresExactlyOneOfWorkerOrTool(t *testing.T) {
	base := func(n domain.Node) *domain.Workflow {
		return &domain.Workflow{
			ID: "ref", Version: "1.0.0",
			Nodes:  []domain.Node{n},
			Budget: domain.Budget{MaxCostUSD: 1, MaxTokens: 1, MaxDurationMs: 1, MaxRetriesPerNode: 1},
		}
	}

	t.Run("neither is rejected", func(t *testing.T) {
		err := validate.Graph(base(domain.Node{ID: "a"}), nil)
		if err == nil || !strings.Contains(err.Error(), `"a" references neither a worker nor a tool`) {
			t.Errorf("expected a neither-worker-nor-tool error, got: %v", err)
		}
	})

	t.Run("both is rejected", func(t *testing.T) {
		n := domain.Node{ID: "a", Worker: "w@1", Tool: &domain.ToolCall{ToolName: "terminal", Input: map[string]any{"command": "echo"}}}
		err := validate.Graph(base(n), nil)
		if err == nil || !strings.Contains(err.Error(), `"a" references both a worker and a tool`) {
			t.Errorf("expected a both-worker-and-tool error, got: %v", err)
		}
	})

	t.Run("worker only is valid", func(t *testing.T) {
		if err := validate.Graph(base(domain.Node{ID: "a", Worker: "w@1"}), nil); err != nil {
			t.Errorf("worker-only node should be valid, got: %v", err)
		}
	})

	t.Run("tool only is valid", func(t *testing.T) {
		n := domain.Node{ID: "a", Tool: &domain.ToolCall{ToolName: "terminal", Input: map[string]any{"command": "echo"}}}
		if err := validate.Graph(base(n), nil); err != nil {
			t.Errorf("tool-only node should be valid, got: %v", err)
		}
	})
}

func TestGraphRejectsContextArtifactNotUpstream(t *testing.T) {
	// c reads an artifact from b, but b is a sibling (not upstream of c).
	wf := &domain.Workflow{
		ID: "ctx", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "a", Worker: "w@1"},
			{ID: "b", Worker: "w@1"},
			{ID: "c", Worker: "w@1", ContextPolicy: &domain.ContextPolicy{
				Mode:   domain.ContextArtifacts,
				Params: &domain.ContextPolicyParams{Artifacts: []string{"b"}},
			}},
		},
		Edges: []domain.Edge{
			{From: "a", To: "b"},
			{From: "a", To: "c"},
		},
		Budget: domain.Budget{MaxCostUSD: 1, MaxTokens: 1, MaxDurationMs: 1, MaxRetriesPerNode: 1},
	}
	err := validate.Graph(wf, nil)
	if err == nil || !strings.Contains(err.Error(), "not upstream") {
		t.Errorf("expected an upstream-artifact error, got: %v", err)
	}
}

func TestGraphRejectsUndeclaredInputRef(t *testing.T) {
	wf := &domain.Workflow{
		ID: "inputs", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "fetch", Tool: &domain.ToolCall{
				ToolName: "http",
				Input:    map[string]any{"url": "${input:prUrl}"},
			}},
		},
		Edges:  []domain.Edge{},
		Budget: domain.Budget{MaxCostUSD: 1, MaxTokens: 1, MaxDurationMs: 1, MaxRetriesPerNode: 1},
	}
	err := validate.Graph(wf, nil)
	if err == nil || !strings.Contains(err.Error(), "undeclared workflow input") {
		t.Errorf("expected an undeclared-workflow-input error, got: %v", err)
	}
}

func TestGraphAcceptsDeclaredInputRef(t *testing.T) {
	wf := &domain.Workflow{
		ID: "inputs", Version: "1.0.0",
		Nodes: []domain.Node{
			{ID: "fetch", Tool: &domain.ToolCall{
				ToolName: "http",
				Input:    map[string]any{"url": "${input:prUrl}"},
			}},
		},
		Edges:  []domain.Edge{},
		Inputs: []domain.InputDecl{{Name: "prUrl", Required: true}},
		Budget: domain.Budget{MaxCostUSD: 1, MaxTokens: 1, MaxDurationMs: 1, MaxRetriesPerNode: 1},
	}
	if err := validate.Graph(wf, nil); err != nil {
		t.Errorf("expected a declared input reference to validate cleanly, got: %v", err)
	}
}
