// Package git is a Tool exposing a fixed, safe subset of git — status, diff,
// add, commit, branch — run inside the workspace directory. There is
// deliberately NO push in Phase 1 (matches ROADMAP.md): the engine never
// reaches a remote. Each op maps to one git subcommand from a closed set; no
// arbitrary git invocation is possible.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// Tool runs a closed set of git subcommands inside workdir.
type Tool struct {
	workdir string
	timeout time.Duration
}

// New builds a git tool operating in workdir. timeout bounds each git run
// (0 → 30s).
func New(workdir string, timeout time.Duration) *Tool {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Tool{workdir: workdir, timeout: timeout}
}

var _ tool.Tool = (*Tool)(nil)
var _ tool.MutationDescriber = (*Tool)(nil)

func (t *Tool) Name() string    { return "git" }
func (t *Tool) Version() string { return "1.0.0" }

func (t *Tool) InputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["op"],
  "properties": {
    "op": { "enum": ["status", "diff", "add", "commit", "branch"] },
    "paths": { "type": "array", "items": { "type": "string" } },
    "message": { "type": "string", "maxLength": 2000 },
    "name": { "type": "string" }
  }
}`)
}

func (t *Tool) OutputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["op", "output"],
  "properties": {
    "op": { "type": "string" },
    "output": { "type": "string" }
  }
}`)
}

type request struct {
	Op      string   `json:"op"`
	Paths   []string `json:"paths"`
	Message string   `json:"message"`
	Name    string   `json:"name"`
}

// Execute maps op to a fixed git subcommand. Any op outside the closed set — and
// in particular push — is impossible: the argv is built here, never taken from
// the caller.
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("git: decode input: %w", err)
	}

	var args []string
	switch req.Op {
	case "status":
		args = []string{"status", "--porcelain"}
	case "diff":
		args = []string{"diff"}
	case "add":
		if len(req.Paths) == 0 {
			return nil, fmt.Errorf("git: add requires at least one path")
		}
		args = append([]string{"add", "--"}, req.Paths...)
	case "commit":
		if req.Message == "" {
			return nil, fmt.Errorf("git: commit requires a message")
		}
		args = []string{"commit", "-m", req.Message}
	case "branch":
		if req.Name == "" {
			args = []string{"branch"} // list branches
		} else {
			args = []string{"checkout", "-b", req.Name} // create + switch
		}
	default:
		// Unreachable if the input schema held, but defend anyway: no push, no
		// arbitrary subcommand.
		return nil, fmt.Errorf("git: unsupported op %q", req.Op)
	}

	out, err := t.run(ctx, args)
	if err != nil {
		return nil, err
	}
	res, err := json.Marshal(map[string]string{"op": req.Op, "output": out})
	if err != nil {
		return nil, fmt.Errorf("git: encode output: %w", err)
	}
	return res, nil
}

// DescribeMutation classifies status/diff as read-only and add/commit/branch
// creation as mutating.
func (t *Tool) DescribeMutation(input json.RawMessage) (tool.Mutation, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return tool.Mutation{}, fmt.Errorf("git: decode mutation input: %w", err)
	}
	switch req.Op {
	case "status", "diff":
		return tool.Mutation{Mutating: false, Operation: "git." + req.Op, Paths: req.Paths}, nil
	case "add":
		return tool.Mutation{Mutating: true, Operation: "git.add", Summary: "stage paths", Paths: req.Paths}, nil
	case "commit":
		return tool.Mutation{Mutating: true, Operation: "git.commit", Summary: "create a local commit"}, nil
	case "branch":
		if req.Name == "" {
			return tool.Mutation{Mutating: false, Operation: "git.branch"}, nil
		}
		return tool.Mutation{Mutating: true, Operation: "git.branch", Summary: "create and switch to branch " + req.Name}, nil
	default:
		return tool.Mutation{}, fmt.Errorf("git: unsupported op %q", req.Op)
	}
}

func (t *Tool) run(ctx context.Context, args []string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = t.workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git %s: timed out after %s", args[0], t.timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s failed (exit %d): %s", args[0], ee.ExitCode(), stderr.String())
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.String(), nil
}
