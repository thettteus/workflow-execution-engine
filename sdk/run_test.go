package sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool"
	"github.com/thettteus/workflow-execution-engine/core/tool/terminal"
	"github.com/thettteus/workflow-execution-engine/sdk"
)

// echoWorkflow builds a one-node tool workflow that runs `echo`, so a run can be
// tested end to end with no model call and no API key.
func echoWorkflow(t *testing.T) *sdk.Workflow {
	t.Helper()
	wf, err := sdk.New("demo", "1.0.0").
		Budget(domain.Budget{MaxCostUSD: 1, MaxTokens: 100, MaxDurationMs: 30000, MaxRetriesPerNode: 0}).
		Tool("echo", domain.ToolCall{ToolName: "terminal", Input: map[string]any{"command": "echo", "args": []any{"hi from sdk"}}}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return wf
}

func toolRegistry(dir string) *tool.Registry {
	r := tool.NewRegistry()
	r.Register(terminal.New(dir, []string{"echo"}, 30*time.Second, domain.ArtifactTestResult))
	return r
}

// TestRunStreamsEventsAndCompletes is the REQ-SDK-01 Run path plus event
// subscription: a run streams events on Events() and Wait() returns a succeeded
// result.
func TestRunStreamsEventsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	exec, err := echoWorkflow(t).Run(context.Background(), sdk.RunOptions{Workspace: dir, Tools: toolRegistry(dir), AllowMutationsWithoutApproval: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sawFinish bool
	for ev := range exec.Events() {
		if ev.Type == domain.ExecutionFinished {
			sawFinish = true
		}
	}
	if !sawFinish {
		t.Error("expected an ExecutionFinished event on the stream")
	}

	res, err := exec.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Errorf("state = %s, want succeeded", res.State)
	}
}

// TestArtifactTypedAccess is the REQ-SDK-02 acceptance path: a node's artifact
// decodes into a caller-supplied Go type via generics.
func TestArtifactTypedAccess(t *testing.T) {
	dir := t.TempDir()
	exec, err := echoWorkflow(t).Run(context.Background(), sdk.RunOptions{Workspace: dir, Tools: toolRegistry(dir), AllowMutationsWithoutApproval: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := exec.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	type termResult struct {
		Command  string `json:"command"`
		ExitCode int    `json:"exitCode"`
		Passed   bool   `json:"passed"`
		Stdout   string `json:"stdout"`
	}
	got, err := sdk.Artifact[termResult](exec, "echo")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if !got.Passed || got.ExitCode != 0 {
		t.Errorf("expected a passing echo, got %+v", got)
	}
	if got.Stdout != "hi from sdk\n" {
		t.Errorf("stdout = %q, want %q", got.Stdout, "hi from sdk\n")
	}
}

// TestArtifactUnknownNodeErrors: asking for a node that isn't in the graph is an
// error, not a zero value.
func TestArtifactUnknownNodeErrors(t *testing.T) {
	dir := t.TempDir()
	exec, err := echoWorkflow(t).Run(context.Background(), sdk.RunOptions{Workspace: dir, Tools: toolRegistry(dir), AllowMutationsWithoutApproval: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := exec.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, err := sdk.Artifact[map[string]any](exec, "ghost"); err == nil {
		t.Error("expected an error for an unknown node")
	}
}
