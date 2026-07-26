package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// fakeTool is a scriptable Tool for exercising Invoke without touching the world.
type fakeTool struct {
	in, out []byte
	ran     bool
	runErr  error
	output  json.RawMessage
}

func (f *fakeTool) Name() string         { return "fake" }
func (f *fakeTool) Version() string      { return "1.0.0" }
func (f *fakeTool) InputSchema() []byte  { return f.in }
func (f *fakeTool) OutputSchema() []byte { return f.out }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	f.ran = true
	if f.runErr != nil {
		return nil, f.runErr
	}
	return f.output, nil
}

// recorder collects emitted events in order.
type recorder struct{ events []domain.Event }

func (r *recorder) emit(t domain.EventType, payload map[string]any) {
	r.events = append(r.events, domain.Event{Type: t, Payload: payload})
}

func (r *recorder) types() []domain.EventType {
	out := make([]domain.EventType, len(r.events))
	for i, e := range r.events {
		out[i] = e.Type
	}
	return out
}

const objInputSchema = `{"type":"object","required":["x"],"properties":{"x":{"type":"number"}}}`
const objOutputSchema = `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`

func TestInvokeHappyPathEmitsEventPair(t *testing.T) {
	ft := &fakeTool{in: []byte(objInputSchema), out: []byte(objOutputSchema), output: []byte(`{"ok":true}`)}
	rec := &recorder{}
	out, err := tool.Invoke(context.Background(), ft, json.RawMessage(`{"x":1}`), rec.emit, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("output = %s", out)
	}
	if got := rec.types(); len(got) != 2 || got[0] != domain.ToolCalled || got[1] != domain.ToolResult {
		t.Errorf("events = %v, want [ToolCalled ToolResult]", got)
	}
	// ToolResult carries the output; ToolCalled carries the input.
	if _, ok := rec.events[1].Payload["output"]; !ok {
		t.Errorf("ToolResult missing output payload: %v", rec.events[1].Payload)
	}
}

func TestInvokeRejectsBadInputBeforeExecute(t *testing.T) {
	ft := &fakeTool{in: []byte(objInputSchema), out: []byte(objOutputSchema), output: []byte(`{"ok":true}`)}
	rec := &recorder{}
	_, err := tool.Invoke(context.Background(), ft, json.RawMessage(`{"x":"not a number"}`), rec.emit, nil)
	if err == nil {
		t.Fatal("invalid input must be rejected")
	}
	if ft.ran {
		t.Error("Execute must not run when input fails its schema")
	}
	// A rejected call still records a ToolResult (with the error) but no ToolCalled.
	if got := rec.types(); len(got) != 1 || got[0] != domain.ToolResult {
		t.Errorf("events = %v, want [ToolResult] for a pre-execution rejection", got)
	}
	diag, ok := rec.events[0].Payload["diagnostic"].(map[string]any)
	if !ok || diag["code"] != "tool_input_schema_invalid" || diag["likelyFix"] == "" {
		t.Fatalf("diagnostic = %#v", rec.events[0].Payload["diagnostic"])
	}
}

func TestInvokeRejectsBadOutput(t *testing.T) {
	ft := &fakeTool{in: []byte(objInputSchema), out: []byte(objOutputSchema), output: []byte(`{"ok":"nope"}`)}
	rec := &recorder{}
	_, err := tool.Invoke(context.Background(), ft, json.RawMessage(`{"x":1}`), rec.emit, nil)
	if err == nil {
		t.Fatal("output violating the schema must fail the call")
	}
	if !ft.ran {
		t.Error("Execute should have run")
	}
}

func TestInvokePropagatesExecuteError(t *testing.T) {
	sentinel := errors.New("boom")
	ft := &fakeTool{in: []byte(objInputSchema), out: []byte(objOutputSchema), runErr: sentinel}
	rec := &recorder{}
	_, err := tool.Invoke(context.Background(), ft, json.RawMessage(`{"x":1}`), rec.emit, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want boom", err)
	}
	last := rec.events[len(rec.events)-1]
	if last.Type != domain.ToolResult || last.Payload["error"] == nil {
		t.Errorf("failing call should record a ToolResult with an error, got %v", last)
	}
}

func TestRegistry(t *testing.T) {
	r := tool.NewRegistry()
	ft := &fakeTool{}
	r.Register(ft)
	if got, ok := r.Get("fake"); !ok || got != ft {
		t.Errorf("registry did not return the registered tool")
	}
	if _, ok := r.Get("missing"); ok {
		t.Errorf("registry returned a tool that was never registered")
	}
}
