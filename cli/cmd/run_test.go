package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// toolWorkflow is a single deterministic tool node — it runs with no model
// call and no API key, so a run's full assembly/stream/exit path can be tested
// end to end. wee.yaml allowlists the commands it uses.
const toolWorkflow = `id: toolflow
version: 1.0.0
nodes:
  - id: echo
    tool:
      toolName: terminal
      input:
        command: echo
        args: ["hello from wee"]
edges: []
budget:
  maxCostUsd: 1.0
  maxTokens: 1000
  maxDurationMs: 30000
  maxRetriesPerNode: 0
`

const weeConfig = "terminal:\n  allow: [echo, sleep]\n"

// setupToolRun writes a tool workflow + wee.yaml into a temp dir and chdirs
// there, returning the workflow path (relative). Runs need cwd to hold both the
// workflow and .workflow/ state dir.
func setupToolRun(t *testing.T, workflow string) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wee.yaml"), []byte(weeConfig), 0o644); err != nil {
		t.Fatalf("write wee.yaml: %v", err)
	}
	return "wf.yaml"
}

// TestRunToolWorkflowSucceeds is the exit-0 end-to-end path (REQ-CLI-04) plus
// the --json contract (REQ-CLI-03): a deterministic tool run completes, and its
// --json output is line-delimited JSON that decodes to domain.Event, bracketed
// by ExecutionStarted/ExecutionFinished.
func TestRunToolWorkflowSucceeds(t *testing.T) {
	wf := setupToolRun(t, toolWorkflow)
	out, err := execCLI(t, "run", wf, "--json", "--allow-mutations-without-approval")
	if err != nil {
		t.Fatalf("run returned: %v\noutput:\n%s", err, out)
	}

	var sawStart, sawFinish bool
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev domain.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("--json line is not valid Event JSON: %q: %v", line, err)
		}
		switch ev.Type {
		case domain.ExecutionStarted:
			sawStart = true
		case domain.ExecutionFinished:
			sawFinish = true
			if ev.Payload["state"] != string(domain.ExecutionSucceeded) {
				t.Errorf("final state = %v, want succeeded", ev.Payload["state"])
			}
		}
	}
	if !sawStart || !sawFinish {
		t.Errorf("expected ExecutionStarted and ExecutionFinished in the stream (start=%v finish=%v)", sawStart, sawFinish)
	}
}

// inputWorkflow declares one required input and references it from a
// terminal tool call — REQ-INPUT-01's end-to-end CLI path.
const inputWorkflow = `id: inputflow
version: 1.0.0
nodes:
  - id: echo
    tool:
      toolName: terminal
      input:
        command: echo
        args: ["${input:msg}"]
edges: []
inputs:
  - name: msg
    required: true
budget:
  maxCostUsd: 1.0
  maxTokens: 1000
  maxDurationMs: 30000
  maxRetriesPerNode: 0
`

// TestRunInputFlagResolvesPlaceholder is REQ-INPUT-01's CLI path: --input
// supplies the value a "${input:NAME}" placeholder resolves to.
func TestRunInputFlagResolvesPlaceholder(t *testing.T) {
	wf := setupToolRun(t, inputWorkflow)
	out, err := execCLI(t, "run", wf, "--json", "--input", "msg=hello from input", "--allow-mutations-without-approval")
	if err != nil {
		t.Fatalf("run returned: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, `"state":"succeeded"`) {
		t.Errorf("run did not succeed, output:\n%s", out)
	}
}

// TestRunMissingRequiredInputExits3 is REQ-INPUT-01's fail-fast half: a
// required input with no --input and no default is a validation-class
// failure (exit 3), not a node failure.
func TestRunMissingRequiredInputExits3(t *testing.T) {
	wf := setupToolRun(t, inputWorkflow)
	_, err := execCLI(t, "run", wf)
	assertExit(t, err, ExitValidation)
}

// TestRunUnregisteredWorkerExits1 forces a node failure (REQ-CLI-04 exit 1): a
// worker-backed node whose Worker file is absent fails to resolve at execution.
func TestRunUnregisteredWorkerExits1(t *testing.T) {
	wf := "id: wf\nversion: 1.0.0\nnodes:\n  - id: a\n    worker: ghost@1.0.0\nedges: []\nbudget:\n  maxCostUsd: 1\n  maxTokens: 10\n  maxDurationMs: 1000\n  maxRetriesPerNode: 0\n"
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(wf), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := execCLI(t, "run", "wf.yaml")
	assertExit(t, err, ExitNodeFailure)
}

// TestRunInvalidWorkflowExits3 forces a validation error (REQ-CLI-04 exit 3).
func TestRunInvalidWorkflowExits3(t *testing.T) {
	bad := "id: bad\nversion: 1.0.0\nnodes:\n  - id: orphan\nedges: []\nbudget:\n  maxCostUsd: 1\n  maxTokens: 1\n  maxDurationMs: 1\n  maxRetriesPerNode: 0\n"
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := execCLI(t, "run", "bad.yaml")
	assertExit(t, err, ExitValidation)
}

// TestRunCancelledExits130 forces the cancellation path (REQ-CLI-04 exit 130):
// a blocking tool node is interrupted by cancelling the command context — the
// same context SIGINT cancels — and the run finalizes as cancelled.
func TestRunCancelledExits130(t *testing.T) {
	blocking := "id: slow\nversion: 1.0.0\nnodes:\n  - id: wait\n    tool:\n      toolName: terminal\n      input:\n        command: sleep\n        args: [\"30\"]\nedges: []\nbudget:\n  maxCostUsd: 1\n  maxTokens: 1\n  maxDurationMs: 60000\n  maxRetriesPerNode: 0\n"
	setupToolRun(t, blocking)

	ctx, cancel := context.WithCancel(context.Background())
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"run", "wf.yaml", "--allow-mutations-without-approval"})
	root.SetContext(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- root.Execute() }()
	time.Sleep(200 * time.Millisecond) // let the node start blocking
	cancel()

	select {
	case err := <-errCh:
		assertExit(t, err, ExitCancelled)
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

// TestExitForRunMapping covers the two engine outcomes that are impractical to
// force end-to-end in a unit test: a budget-exceeded run (needs a priced model
// call) and a cancelled run reported via context.Canceled. The exit-0/1/3 and
// context-cancel(130) paths above exercise the real end-to-end flow.
func TestExitForRunMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"budget", engine.ErrBudgetExceeded, ExitBudget},
		{"cancelled-sentinel", engine.ErrCancelled, ExitCancelled},
		{"cancelled-ctx", context.Canceled, ExitCancelled},
		{"node-failure", engine.ErrNodeFailed, ExitNodeFailure},
		{"success", nil, ExitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exitForRun(nil, tc.err)
			if tc.want == ExitOK {
				if got != nil {
					t.Fatalf("want nil (exit 0), got %v", got)
				}
				return
			}
			var ce *CodedError
			if !errors.As(got, &ce) {
				t.Fatalf("want *CodedError, got %T: %v", got, got)
			}
			if ce.Code != tc.want {
				t.Errorf("exit code = %d, want %d", ce.Code, tc.want)
			}
		})
	}
}

func assertExit(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with exit code %d, got nil", want)
	}
	var ce *CodedError
	if !errors.As(err, &ce) {
		t.Fatalf("want *CodedError, got %T: %v", err, err)
	}
	if ce.Code != want {
		t.Errorf("exit code = %d, want %d (err: %v)", ce.Code, want, err)
	}
}
