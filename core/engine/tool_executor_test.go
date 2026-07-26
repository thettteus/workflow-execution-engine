package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// fakeTool is a minimal tool.Tool for exercising ToolExecutor without a real
// filesystem/terminal/git/http implementation. It has no ArtifactType()
// method — the "default to ArtifactJSON" path.
type fakeTool struct {
	name      string
	execFn    func(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
	lastInput json.RawMessage
}

func (f *fakeTool) Name() string    { return f.name }
func (f *fakeTool) Version() string { return "1.0.0" }
func (f *fakeTool) InputSchema() []byte {
	return []byte(`{"type":"object"}`)
}
func (f *fakeTool) OutputSchema() []byte {
	return []byte(`{"type":"object"}`)
}
func (f *fakeTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	f.lastInput = input
	if f.execFn != nil {
		return f.execFn(ctx, input)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// fakeTypedTool additionally implements the optional ArtifactType() capability.
type fakeTypedTool struct {
	fakeTool
	artifactType domain.ArtifactType
}

func (f *fakeTypedTool) ArtifactType() domain.ArtifactType { return f.artifactType }

func registryWith(t tool.Tool) *tool.Registry {
	r := tool.NewRegistry()
	r.Register(t)
	return r
}

func TestToolExecutorInvokesRegisteredTool(t *testing.T) {
	ft := &fakeTool{name: "fake"}
	ex := engine.NewToolExecutor(registryWith(ft))

	node := domain.Node{ID: "run", Tool: &domain.ToolCall{ToolName: "fake", Input: map[string]any{"x": float64(1)}}}
	res, err := ex.Execute(context.Background(), engine.NodeRequest{Node: node})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(res.Content) != `{"ok":true}` {
		t.Errorf("Content = %s", res.Content)
	}
	if res.Type != domain.ArtifactJSON {
		t.Errorf("Type = %s, want default ArtifactJSON (tool has no ArtifactType() capability)", res.Type)
	}
	if res.CostUSD != 0 || res.Tokens != 0 {
		t.Errorf("tool-backed node should never carry model cost/tokens: %+v", res)
	}
}

func TestToolExecutorUsesToolArtifactTypeCapability(t *testing.T) {
	ft := &fakeTypedTool{fakeTool: fakeTool{name: "typed"}, artifactType: domain.ArtifactTestResult}
	ex := engine.NewToolExecutor(registryWith(ft))

	node := domain.Node{ID: "run", Tool: &domain.ToolCall{ToolName: "typed", Input: map[string]any{}}}
	res, err := ex.Execute(context.Background(), engine.NodeRequest{Node: node})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Type != domain.ArtifactTestResult {
		t.Errorf("Type = %s, want the tool's own ArtifactType() (test-result)", res.Type)
	}
}

// TestToolExecutorResolvesUpstreamPlaceholder confirms the node's static Input
// is resolved against upstream artifacts before the tool ever sees it, and the
// referenced hash is recorded in ContextHashes.
func TestToolExecutorResolvesUpstreamPlaceholder(t *testing.T) {
	ft := &fakeTool{name: "fake"}
	ex := engine.NewToolExecutor(registryWith(ft))

	node := domain.Node{ID: "commit", Tool: &domain.ToolCall{
		ToolName: "fake",
		Input:    map[string]any{"message": "${fixer.summary}"},
	}}
	req := engine.NodeRequest{
		Node:   node,
		Inputs: []engine.NodeInput{{FromNode: "fixer", Content: []byte(`{"summary":"fixed it"}`), Hash: "h-fixer"}},
	}
	res, err := ex.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(ft.lastInput, &got)
	if got["message"] != "fixed it" {
		t.Errorf("tool received unresolved input: %s", ft.lastInput)
	}
	if len(res.ContextHashes) != 1 || res.ContextHashes[0] != "h-fixer" {
		t.Errorf("ContextHashes = %v, want [h-fixer]", res.ContextHashes)
	}
}

func TestToolExecutorResolvesConnectionPlaceholders(t *testing.T) {
	ft := &fakeTool{name: "fake"}
	ex := engine.NewToolExecutor(registryWith(ft))
	node := domain.Node{ID: "fetch", Tool: &domain.ToolCall{
		ToolName: "fake",
		Input: map[string]any{
			"url":       "${connection:gitlab.baseUrl}",
			"secretEnv": "${connection:gitlab.secretEnv}",
			"header":    "${connection:gitlab.tokenHeader}",
		},
	}}
	req := engine.NodeRequest{
		Node: node,
		ConnectionRefs: map[string]engine.ConnectionRef{
			"gitlab": {
				ID:        "gitlab",
				Kind:      "change-source",
				Type:      "gitlab",
				BaseURL:   "https://gitlab.example/api/v4",
				SecretEnv: "GITLAB_AUTH_HEADER",
				Defaults:  map[string]string{"tokenHeader": "PRIVATE-TOKEN"},
			},
		},
	}
	if _, err := ex.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(ft.lastInput, &got)
	if got["url"] != "https://gitlab.example/api/v4" || got["secretEnv"] != "GITLAB_AUTH_HEADER" || got["header"] != "PRIVATE-TOKEN" {
		t.Fatalf("tool received unresolved connection fields: %s", ft.lastInput)
	}
}

func TestToolExecutorNotToolBackedIsFatal(t *testing.T) {
	ex := engine.NewToolExecutor(tool.NewRegistry())
	_, err := ex.Execute(context.Background(), engine.NodeRequest{Node: domain.Node{ID: "a", Worker: "w@1"}})
	if err == nil {
		t.Fatal("expected an error for a non-tool-backed node")
	}
}

func TestToolExecutorUnknownToolIsError(t *testing.T) {
	ex := engine.NewToolExecutor(tool.NewRegistry())
	node := domain.Node{ID: "a", Tool: &domain.ToolCall{ToolName: "nope", Input: map[string]any{}}}
	_, err := ex.Execute(context.Background(), engine.NodeRequest{Node: node})
	if err == nil {
		t.Fatal("expected an error for an unregistered tool name")
	}
}

// TestToolExecutorEmitsEventPair is REQ-TOOL-02, wired into the graph for the
// first time (M1.6a): ExecuteWithEmit produces a ToolCalled/ToolResult pair.
func TestToolExecutorEmitsEventPair(t *testing.T) {
	ft := &fakeTool{name: "fake"}
	ex := engine.NewToolExecutor(registryWith(ft))
	node := domain.Node{ID: "run", Tool: &domain.ToolCall{ToolName: "fake", Input: map[string]any{}}}

	var events []domain.EventType
	emit := func(t domain.EventType, _ map[string]any) { events = append(events, t) }
	_, err := ex.ExecuteWithEmit(context.Background(), engine.NodeRequest{Node: node}, emit)
	if err != nil {
		t.Fatalf("ExecuteWithEmit: %v", err)
	}
	if len(events) != 2 || events[0] != domain.ToolCalled || events[1] != domain.ToolResult {
		t.Errorf("events = %v, want [ToolCalled ToolResult]", events)
	}
}
