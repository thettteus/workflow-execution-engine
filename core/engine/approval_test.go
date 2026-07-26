package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/tool"
	"github.com/thettteus/workflow-execution-engine/core/tool/filesystem"
)

func approvalScheduler(t *testing.T) (*engine.Scheduler, *eventlog.Log, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := tool.NewRegistry()
	tools.Register(filesystem.New(root))
	log := eventlog.New(base)
	s := engine.New(engine.NewToolExecutor(tools), store.New(base), log, cache.New(base))
	return s, log, root
}

func writeWorkflow() *domain.Workflow {
	return &domain.Workflow{
		ID: "mutate", Version: "1.0.0",
		Nodes: []domain.Node{{
			ID: "write",
			Tool: &domain.ToolCall{ToolName: "filesystem", Input: map[string]any{
				"op":      "write",
				"path":    "out.txt",
				"content": "approved",
			}},
		}},
	}
}

func TestMutatingToolPausesBeforeToolCalledUntilApproved(t *testing.T) {
	s, log, root := approvalScheduler(t)
	res, err := s.Run(context.Background(), writeWorkflow(), engine.RunOptions{ExecutionID: "e1"})
	if !errors.Is(err, engine.ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	if res.State != domain.ExecutionPaused {
		t.Fatalf("state = %s, want paused", res.State)
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was written before approval: %v", err)
	}
	events, err := log.ReadAll("e1")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint string
	for _, ev := range events {
		if ev.Type == domain.ToolCalled {
			t.Fatal("ToolCalled was emitted before approval")
		}
		if ev.Type == domain.ApprovalRequested {
			checkpoint, _ = ev.Payload["checkpointId"].(string)
		}
	}
	if checkpoint == "" {
		t.Fatalf("missing ApprovalRequested event: %#v", events)
	}

	if err := log.Append("e1", domain.Event{
		Type:        domain.ApprovalGranted,
		ExecutionID: "e1",
		NodeID:      "write",
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"checkpointId": checkpoint, "tool": "filesystem", "status": "granted"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Resume(context.Background(), "e1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "approved" {
		t.Fatalf("content = %q", data)
	}
}

func TestRejectedApprovalFailsWithoutToolCalled(t *testing.T) {
	s, log, root := approvalScheduler(t)
	_, err := s.Run(context.Background(), writeWorkflow(), engine.RunOptions{ExecutionID: "e1"})
	if !errors.Is(err, engine.ErrApprovalRequired) {
		t.Fatalf("err = %v, want approval required", err)
	}
	events, _ := log.ReadAll("e1")
	checkpoint, _ := events[2].Payload["checkpointId"].(string)
	if err := log.Append("e1", domain.Event{
		Type:        domain.ApprovalRejected,
		ExecutionID: "e1",
		NodeID:      "write",
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"checkpointId": checkpoint, "tool": "filesystem", "status": "rejected"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Resume(context.Background(), "e1")
	if !errors.Is(err, engine.ErrApprovalRejected) {
		t.Fatalf("err = %v, want approval rejected", err)
	}
	if res.State != domain.ExecutionFailed {
		t.Fatalf("state = %s, want failed", res.State)
	}
	events, _ = log.ReadAll("e1")
	for _, ev := range events {
		if ev.Type == domain.ToolCalled {
			t.Fatal("ToolCalled was emitted after rejection")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file was written despite rejection: %v", err)
	}
}

func TestUnattendedMutationOptInBypassesApproval(t *testing.T) {
	s, log, root := approvalScheduler(t)
	res, err := s.Run(context.Background(), writeWorkflow(), engine.RunOptions{ExecutionID: "e1", AllowUnattendedMutations: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); err != nil {
		t.Fatalf("file was not written with explicit opt-in: %v", err)
	}
	if eventCount(t, log, "e1", domain.ApprovalRequested) != 0 {
		t.Fatal("approval request emitted despite unattended opt-in")
	}
}
