// Package terminal is a Tool that runs a command from a per-workflow allowlist,
// in the workspace directory, under a timeout, capturing stdout/stderr
// (REQ-TOOL-03, PRIN-10). A command not on the allowlist fails with a distinct
// error and is never attempted. The result is wrapped as a test-result or file
// artifact depending on how the tool is configured.
package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// Tool runs allowlisted commands inside a working directory.
type Tool struct {
	workdir      string
	allow        map[string]bool
	timeout      time.Duration
	artifactType domain.ArtifactType
}

// New builds a terminal tool. allow is the set of permitted command names
// (argv[0]); timeout bounds each run (0 → 30s); artifactType is the kind of
// artifact the captured result represents (test-result or file). now is
// injectable for tests; nil → time.Now.
func New(workdir string, allow []string, timeout time.Duration, artifactType domain.ArtifactType) *Tool {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	set := make(map[string]bool, len(allow))
	for _, c := range allow {
		set[c] = true
	}
	return &Tool{workdir: workdir, allow: set, timeout: timeout, artifactType: artifactType}
}

var _ tool.Tool = (*Tool)(nil)
var _ tool.MutationDescriber = (*Tool)(nil)

func (t *Tool) Name() string    { return "terminal" }
func (t *Tool) Version() string { return "1.0.0" }

// ArtifactType is the artifact kind this tool's output represents. It is not
// part of the Tool interface — the engine reads it when storing the result.
func (t *Tool) ArtifactType() domain.ArtifactType { return t.artifactType }

func (t *Tool) InputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["command"],
  "properties": {
    "command": { "type": "string" },
    "args": { "type": "array", "items": { "type": "string" } }
  }
}`)
}

func (t *Tool) OutputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["command", "exitCode", "passed", "stdout", "stderr", "durationMs"],
  "properties": {
    "command": { "type": "string" },
    "exitCode": { "type": "integer" },
    "passed": { "type": "boolean" },
    "stdout": { "type": "string" },
    "stderr": { "type": "string" },
    "durationMs": { "type": "integer" }
  }
}`)
}

type request struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type result struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exitCode"`
	Passed     bool   `json:"passed"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs"`
}

// Execute runs the command if it is allowlisted. A non-zero exit is a normal
// (captured) result, not a tool error — the caller reads passed/exitCode; only
// a disallowed command or a failure to launch is an error.
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("terminal: decode input: %w", err)
	}
	if !t.allow[req.Command] {
		return nil, fmt.Errorf("terminal: command %q is not on the workflow allowlist", req.Command)
	}

	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = t.workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	durationMs := time.Since(start).Milliseconds()

	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("terminal: %q timed out after %s", req.Command, t.timeout)
	}

	res := result{
		Command:    req.Command,
		ExitCode:   exitCode(runErr),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: durationMs,
	}
	res.Passed = res.ExitCode == 0

	// A launch failure (binary missing, permission denied) is a tool error;
	// a non-zero exit from a launched process is a captured, passed=false result.
	if runErr != nil && res.ExitCode < 0 {
		return nil, fmt.Errorf("terminal: launch %q: %w", req.Command, runErr)
	}

	out, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("terminal: encode output: %w", err)
	}
	return out, nil
}

// DescribeMutation treats terminal commands as mutating by default: arbitrary
// allowlisted commands may touch the workspace even when their name sounds
// read-only.
func (t *Tool) DescribeMutation(input json.RawMessage) (tool.Mutation, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return tool.Mutation{}, fmt.Errorf("terminal: decode mutation input: %w", err)
	}
	cmd := append([]string{req.Command}, req.Args...)
	return tool.Mutation{
		Mutating:  true,
		Operation: "terminal.command",
		Summary:   fmt.Sprintf("run %q in the workspace", req.Command),
		Command:   cmd,
	}, nil
}

// exitCode extracts a process exit code from Run's error. A nil error is 0; an
// *exec.ExitError carries the real code; anything else (couldn't launch) is -1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
