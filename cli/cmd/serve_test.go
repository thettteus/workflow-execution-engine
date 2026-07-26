package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/server"
)

func TestServeCommandRegistered(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "serve" {
			return
		}
	}
	t.Fatal("serve command is not registered on root")
}

// TestServeRunsWorkflowInBackground drives the real serve wiring end to end: the
// runAssembler `wee serve` injects resolves a tool-only workflow (no API key
// needed), and POST /api/run returns an id immediately while the run completes
// in the background, writing the terminal ExecutionFinished the live stream
// tails (REQ-CTRL-03).
func TestServeRunsWorkflowInBackground(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "check.yaml"), `id: build-check
version: 1.0.0
nodes:
  - id: check
    tool:
      toolName: terminal
      input:
        command: echo
        args: ["ok"]
edges: []
budget: {maxCostUsd: 0, maxTokens: 0, maxDurationMs: 30000, maxRetriesPerNode: 1}
`)
	writeFile(t, filepath.Join(dir, "wee.yaml"), "terminal:\n  allow: [\"echo\"]\n  timeoutMs: 5000\n")

	workspace := filepath.Join(dir, ".workflow")
	srv := server.New(server.Config{
		Workspace:    workspace,
		Assemble:     runAssembler(dir, workspace),
		NewID:        runner.NewExecutionID,
		DefaultCache: engine.CacheOff,
		Dir:          dir,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/run", "application/json", strings.NewReader(`{"workflow":"check.yaml","allowMutationsWithoutApproval":true}`))
	if err != nil {
		t.Fatalf("post run: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		ExecutionID string `json:"executionId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ExecutionID == "" {
		t.Fatalf("decode run response: %v (id=%q)", err, out.ExecutionID)
	}

	// The run is asynchronous; poll the log (as the WebSocket handler does) for
	// the terminal event and assert it succeeded.
	log := eventlog.New(workspace)
	deadline := time.Now().Add(3 * time.Second)
	for {
		events, err := log.ReadAll(out.ExecutionID)
		if err == nil && len(events) > 0 && events[len(events)-1].Type == domain.ExecutionFinished {
			if st, _ := events[len(events)-1].Payload["state"].(string); st != string(domain.ExecutionSucceeded) {
				t.Fatalf("run finished in state %q, want succeeded", st)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish; last events: %+v", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunAssemblerRejectsMissingWorkflow(t *testing.T) {
	dir := t.TempDir()
	if _, err := runAssembler(dir, filepath.Join(dir, ".workflow"))("does-not-exist.yaml"); err == nil {
		t.Fatal("expected an error for a missing workflow file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
