package terminal_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool/terminal"
)

type result struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Passed   bool   `json:"passed"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// TestTestResultArtifact is the M1.5 acceptance test: a terminal command (echo
// standing in for `npm test`) produces a test-result-shaped output with a
// pass/fail flag and captured output.
func TestTestResultArtifact(t *testing.T) {
	term := terminal.New(t.TempDir(), []string{"echo"}, 0, domain.ArtifactTestResult)
	if term.ArtifactType() != domain.ArtifactTestResult {
		t.Errorf("artifact type = %q, want test-result", term.ArtifactType())
	}
	out, err := term.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["all tests passed"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var r result
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !r.Passed || r.ExitCode != 0 {
		t.Errorf("expected pass, got exit=%d passed=%v", r.ExitCode, r.Passed)
	}
	if r.Stdout != "all tests passed\n" {
		t.Errorf("stdout not captured: %q", r.Stdout)
	}
}

func TestNonZeroExitIsCapturedNotErrored(t *testing.T) {
	term := terminal.New(t.TempDir(), []string{"sh"}, 0, domain.ArtifactTestResult)
	out, err := term.Execute(context.Background(), json.RawMessage(`{"command":"sh","args":["-c","echo boom >&2; exit 3"]}`))
	if err != nil {
		t.Fatalf("a non-zero exit should be a captured result, not a tool error: %v", err)
	}
	var r result
	_ = json.Unmarshal(out, &r)
	if r.Passed || r.ExitCode != 3 {
		t.Errorf("exit=%d passed=%v, want exit 3 / failed", r.ExitCode, r.Passed)
	}
	if r.Stderr != "boom\n" {
		t.Errorf("stderr not captured: %q", r.Stderr)
	}
}

// TestDisallowedCommandRejected: a command not on the allowlist fails with a
// distinct error and is never run (REQ-TOOL-03).
func TestDisallowedCommandRejected(t *testing.T) {
	term := terminal.New(t.TempDir(), []string{"echo"}, 0, domain.ArtifactTestResult)
	if _, err := term.Execute(context.Background(), json.RawMessage(`{"command":"rm","args":["-rf","/"]}`)); err == nil {
		t.Fatal("a non-allowlisted command must be rejected")
	}
}

func TestTimeoutEnforced(t *testing.T) {
	term := terminal.New(t.TempDir(), []string{"sh"}, 50*time.Millisecond, domain.ArtifactTestResult)
	_, err := term.Execute(context.Background(), json.RawMessage(`{"command":"sh","args":["-c","sleep 5"]}`))
	if err == nil {
		t.Fatal("a command exceeding the timeout must error")
	}
}
