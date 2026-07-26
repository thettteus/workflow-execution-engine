// Package tool is the uniform, schema-validated, sandboxed interface through
// which Workers touch the world — filesystem, terminal, git, HTTP (REQ-TOOL-01,
// REQ-TOOL-04). Nothing here is AI-specific. Every invocation validates its
// input and output against the tool's declared JSON Schemas and emits a
// ToolCalled/ToolResult event pair, so tool activity is fully reconstructable
// from the log (REQ-TOOL-02). Sandboxing is the default, not an option
// (PRIN-10): each built-in tool confines itself (workspace root, command
// allowlist, domain allowlist) and returns a distinct error rather than
// silently reaching outside its bounds (REQ-TOOL-03).
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/diagnostic"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/validate"
)

// Tool is the single interface every tool implements (REQ-TOOL-01). Input and
// output are opaque JSON validated against InputSchema/OutputSchema; Execute is
// the only thing that touches the world, and must respect ctx cancellation.
type Tool interface {
	Name() string
	Version() string
	InputSchema() []byte
	OutputSchema() []byte
	Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// Mutation describes whether a validated tool input would mutate local or
// remote state. It is emitted in ApprovalRequested payloads before ToolCalled.
type Mutation struct {
	Mutating  bool     `json:"mutating"`
	Operation string   `json:"operation,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Command   []string `json:"command,omitempty"`
	Method    string   `json:"method,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// MutationDescriber is an optional tool capability used by the engine to pause
// before mutating operations. Built-in tools implement it for M2.5.
type MutationDescriber interface {
	DescribeMutation(input json.RawMessage) (Mutation, error)
}

// Emit records one event. It matches the engine scheduler's emitter so a tool
// invocation chains into the same execution log; tests pass a lightweight one.
type Emit func(t domain.EventType, payload map[string]any)

// Registry maps a tool name to its implementation. A Worker's tool allowlist is
// resolved against it. Populated at startup, read concurrently thereafter.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: make(map[string]Tool)} }

// Register adds (or replaces) a tool under its Name().
func (r *Registry) Register(t Tool) { r.tools[t.Name()] = t }

// Get returns the tool registered under name.
func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.tools[name]; return t, ok }

// Invoke runs one tool call end to end (REQ-TOOL-01, REQ-TOOL-02):
//  1. validate input against the tool's InputSchema — an invalid call is
//     rejected before Execute touches anything;
//  2. emit ToolCalled (tool, version, input);
//  3. run Execute;
//  4. validate output against the tool's OutputSchema;
//  5. emit ToolResult (outcome or error, duration).
//
// emit may be nil (e.g. a pure unit test that only wants the return value).
// now, when nil, defaults to time.Now — injected in tests for deterministic
// durations. The returned error is the tool's own (sandbox rejection, execution
// failure, or schema violation); the ToolResult event records it either way.
func Invoke(ctx context.Context, t Tool, input json.RawMessage, emit Emit, now func() time.Time) (json.RawMessage, error) {
	if now == nil {
		now = time.Now
	}
	if emit == nil {
		emit = func(domain.EventType, map[string]any) {}
	}

	if err := ValidateInput(t, input); err != nil {
		// Reject before Execute — the call never touches the world.
		e := fmt.Errorf("tool %q: input rejected: %w", t.Name(), err)
		e = diagnostic.Wrap(e, diagnostic.KindValidation, "tool_input_schema_invalid", "", "tool.input.validate", "tool input failed its schema", "fix the tool input fields before running")
		emit(domain.ToolResult, map[string]any{"tool": t.Name(), "error": e.Error(), "diagnostic": diagnostic.Payload(e, ""), "durationMs": int64(0)})
		return nil, e
	}

	emit(domain.ToolCalled, map[string]any{
		"tool":    t.Name(),
		"version": t.Version(),
		"input":   json.RawMessage(input),
	})

	start := now()
	out, err := t.Execute(ctx, input)
	durationMs := now().Sub(start).Milliseconds()

	if err != nil {
		err = diagnostic.Wrap(err, diagnostic.KindTool, "tool_execute_failed", "", "tool.execute", "tool execution failed", "check the tool sandbox configuration and local resource availability")
		emit(domain.ToolResult, map[string]any{"tool": t.Name(), "error": err.Error(), "diagnostic": diagnostic.Payload(err, ""), "durationMs": durationMs})
		return nil, err
	}

	if verr := validateAgainst(t.OutputSchema(), out); verr != nil {
		e := fmt.Errorf("tool %q: output failed its schema: %w", t.Name(), verr)
		e = diagnostic.Wrap(e, diagnostic.KindValidation, "tool_output_schema_invalid", "", "tool.output.validate", "tool output failed its schema", "fix the tool implementation or output schema")
		emit(domain.ToolResult, map[string]any{"tool": t.Name(), "error": e.Error(), "diagnostic": diagnostic.Payload(e, ""), "durationMs": durationMs})
		return nil, e
	}

	emit(domain.ToolResult, map[string]any{
		"tool":       t.Name(),
		"output":     json.RawMessage(out),
		"durationMs": durationMs,
	})
	return out, nil
}

// ValidateInput validates input against t's InputSchema without executing it.
// ToolExecutor uses this before approval classification so an invalid mutating
// call is rejected rather than asking a human to approve nonsense.
func ValidateInput(t Tool, input json.RawMessage) error {
	return validateAgainst(t.InputSchema(), input)
}

// validateAgainst compiles a schema and validates data against it. An empty
// schema means "no constraint" (skip).
func validateAgainst(schema []byte, data []byte) error {
	if len(schema) == 0 {
		return nil
	}
	cs, err := validate.CompileSchemaBytes(schema)
	if err != nil {
		return err
	}
	return cs.ValidateBytes(data)
}
