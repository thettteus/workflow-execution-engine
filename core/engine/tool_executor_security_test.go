package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

const testSecretEnvVar = "WEE_TEST_TOOL_SECRET"

// TestToolExecutionRecordNeverContainsEnvSecretValue is the NFR-SEC-01 e2e
// test for tool-backed nodes (M1.6a): a real execution driven by a tool node
// referencing ${env:...} never contains that secret's value in any file the
// run writes, mirroring openai.TestNoKeyMaterialInExecutionRecord. The fake
// tool echoes its input back as output — the worst case, since both
// ToolCalled's "input" and ToolResult's "output" would carry the resolved
// secret if redaction were missing.
func TestToolExecutionRecordNeverContainsEnvSecretValue(t *testing.T) {
	const secret = "sk-DO-NOT-LEAK-TOOL-0xC0FFEE"
	t.Setenv(testSecretEnvVar, secret)

	ft := &fakeTool{name: "fake", execFn: func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
		return input, nil
	}}
	reg := tool.NewRegistry()
	reg.Register(ft)
	ex := engine.NewToolExecutor(reg)

	base := t.TempDir()
	s := engine.New(ex, store.New(base), eventlog.New(base), cache.New(base))

	wf := &domain.Workflow{
		ID: "sec-tool", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "call", Tool: &domain.ToolCall{
			ToolName: "fake",
			Input:    map[string]any{"authorization": fmt.Sprintf("${env:%s}", testSecretEnvVar)},
		}}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	assertNoSecretOnDisk(t, base, secret)
}

// TestToolErrorRedactsEnvSecret mirrors openai.TestNoHeaderInError: a failing
// tool call's error text must not carry a resolved secret either — this path
// is outside the ToolEmitter bridge (Scheduler.executeNode logs err.Error()
// directly on Failure), so ToolExecutor must redact it before returning.
func TestToolErrorRedactsEnvSecret(t *testing.T) {
	const secret = "sk-DO-NOT-LEAK-ON-ERROR-0xBEEF"
	t.Setenv(testSecretEnvVar, secret)

	ft := &fakeTool{name: "fake", execFn: func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
		var in map[string]any
		_ = json.Unmarshal(input, &in)
		return nil, fmt.Errorf("request with auth %v rejected", in["authorization"])
	}}
	reg := tool.NewRegistry()
	reg.Register(ft)
	ex := engine.NewToolExecutor(reg)

	base := t.TempDir()
	s := engine.New(ex, store.New(base), eventlog.New(base), cache.New(base))

	wf := &domain.Workflow{
		ID: "sec-tool-err", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "call", Tool: &domain.ToolCall{
			ToolName: "fake",
			Input:    map[string]any{"authorization": fmt.Sprintf("${env:%s}", testSecretEnvVar)},
		}}},
	}
	_, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("returned error leaked the secret: %v", err)
	}
	assertNoSecretOnDisk(t, base, secret)
}

func assertNoSecretOnDisk(t *testing.T, base, secret string) {
	t.Helper()
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("secret leaked into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
