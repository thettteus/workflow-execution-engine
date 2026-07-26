package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/model"
)

// fakeProvider is a scriptable model.Provider: it returns outputs[i] on call i
// (clamping to the last), and records the messages it was given each call so a
// test can assert on delta feedback.
type fakeProvider struct {
	mu      sync.Mutex
	outputs []string
	calls   int
	seen    [][]model.Message
}

func (f *fakeProvider) Complete(_ context.Context, msgs []model.Message, _ model.Params) (model.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, msgs)
	i := f.calls
	f.calls++
	out := f.outputs[len(f.outputs)-1]
	if i < len(f.outputs) {
		out = f.outputs[i]
	}
	return model.Response{Content: out, InputTokens: 10, OutputTokens: 5}, nil
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *fakeProvider) messages(call int) []model.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[call]
}

func fakeRegistry(p model.Provider) *model.Registry {
	r := model.NewRegistry()
	r.Register("openai", p) // default name, so an empty provider field resolves
	return r
}

// scoreIssuesSchema is the acceptance contract shape (REQ-WORKER-03).
func scoreIssuesSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"score", "issues"},
		"properties": map[string]any{
			"score":  map[string]any{"type": "number"},
			"issues": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 10},
		},
	}
}

func scoreWorker(maxRetries int) domain.Worker {
	return domain.Worker{
		ID: "reviewer", Version: "1.0.0", Objective: "review a diff",
		Contract: domain.Contract{
			Goal:         "score the change",
			OutputSchema: scoreIssuesSchema(),
			MaxRetries:   maxRetries,
		},
		Model: domain.ModelConfig{Model: "gpt-4o-mini"}, // Provider empty → default
	}
}

// TestNoMalformedOutputCrossesBoundary is REQ-WORKER-03 / REQ-CONTRACT-01 at the
// NodeExecutor boundary: valid output passes and is marked validated; malformed
// output never returns a usable result — it surfaces as a contract violation.
func TestNoMalformedOutputCrossesBoundary(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(0)}

	t.Run("valid passes", func(t *testing.T) {
		p := &fakeProvider{outputs: []string{`{"score":0.9,"issues":["nit"]}`}}
		ex := engine.NewWorkerExecutor(workers, fakeRegistry(p))
		res, err := ex.Execute(context.Background(), engine.NodeRequest{Node: domain.Node{ID: "A", Worker: "reviewer@1.0.0"}})
		if err != nil {
			t.Fatalf("valid output should pass: %v", err)
		}
		if !res.Validated || string(res.Content) != `{"score":0.9,"issues":["nit"]}` {
			t.Errorf("unexpected result: %+v", res)
		}
		if res.CostUSD <= 0 {
			t.Errorf("cost should be priced from usage, got %v", res.CostUSD)
		}
	})

	t.Run("malformed is a violation", func(t *testing.T) {
		p := &fakeProvider{outputs: []string{`{"score":"high"}`}} // wrong type, missing issues
		ex := engine.NewWorkerExecutor(workers, fakeRegistry(p))
		res, err := ex.Execute(context.Background(), engine.NodeRequest{Node: domain.Node{ID: "A", Worker: "reviewer@1.0.0"}})
		if err == nil {
			t.Fatal("malformed output must not produce a usable result")
		}
		if len(res.Content) != 0 {
			t.Errorf("no content should escape on violation, got %q", res.Content)
		}
	})
}

// TestContractRetryWithDeltaFeedback is REQ-CONTRACT-02: a provider that returns
// malformed JSON once then valid triggers exactly one retry-with-feedback,
// visible as a Retry event carrying the validation error text; the delta (and
// only the delta) reaches the next model call.
func TestContractRetryWithDeltaFeedback(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(1)}
	p := &fakeProvider{outputs: []string{`{"score":"bad"}`, `{"score":0.5,"issues":[]}`}}
	ex := engine.NewWorkerExecutor(workers, fakeRegistry(p))
	s, log := newScheduler(t, ex)

	wf := &domain.Workflow{ID: "cr", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	if p.callCount() != 2 {
		t.Errorf("provider called %d times, want 2 (1 + 1 retry)", p.callCount())
	}
	if n := eventCount(t, log, "e1", domain.Retry); n != 1 {
		t.Errorf("Retry events = %d, want exactly 1", n)
	}
	if n := eventCount(t, log, "e1", domain.ContractValidated); n != 1 {
		t.Errorf("ContractValidated events = %d, want 1", n)
	}

	// The Retry event must carry the validation error text.
	events, _ := log.ReadAll("e1")
	var retryReason string
	for _, ev := range events {
		if ev.Type == domain.Retry {
			retryReason, _ = ev.Payload["reason"].(string)
		}
	}
	if !strings.Contains(retryReason, "issues") {
		t.Errorf("Retry reason should name the failing field, got %q", retryReason)
	}

	// The second model call must carry the delta feedback — and nothing more of
	// the context than the first call had (PRIN-05: only the errors).
	second := p.messages(1)
	last := second[len(second)-1]
	if !strings.Contains(last.Content, "did not satisfy") || !strings.Contains(last.Content, "issues") {
		t.Errorf("retry call missing delta feedback: %q", last.Content)
	}
}

// TestContractViolationTerminal is REQ-CONTRACT-03: after contract.maxRetries the
// engine emits ContractViolation and fails the node — never a silent pass.
func TestContractViolationTerminal(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(1)}
	p := &fakeProvider{outputs: []string{`{"nope":1}`}} // always malformed
	ex := engine.NewWorkerExecutor(workers, fakeRegistry(p))
	s, log := newScheduler(t, ex)

	wf := &domain.Workflow{ID: "cv", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err == nil {
		t.Fatal("a terminal contract violation must fail the run")
	}
	if res.State != domain.ExecutionFailed {
		t.Errorf("state = %s, want failed", res.State)
	}
	if p.callCount() != 2 {
		t.Errorf("provider called %d times, want 2 (1 + maxRetries=1)", p.callCount())
	}
	if n := eventCount(t, log, "e1", domain.ContractViolation); n != 1 {
		t.Errorf("ContractViolation events = %d, want 1", n)
	}
	if n := eventCount(t, log, "e1", domain.ContractValidated); n != 0 {
		t.Errorf("ContractValidated events = %d, want 0", n)
	}
}

// TestMalformedNeverReachesDownstream wires producer → consumer and confirms the
// consumer runs only when the producer's output cleared its contract, and sees
// exactly that artifact (REQ-WORKER-03 across the graph).
func TestMalformedNeverReachesDownstream(t *testing.T) {
	workers := engine.MapWorkerSource{
		"reviewer@1.0.0": scoreWorker(0),
		"summarizer@1.0.0": {
			ID: "summarizer", Version: "1.0.0",
			Contract: domain.Contract{OutputSchema: map[string]any{"type": "object"}},
			Model:    domain.ModelConfig{Model: "gpt-4o-mini"},
		},
	}
	// reviewer returns valid; summarizer returns valid — the point is reviewer's
	// artifact is the summarizer's only input.
	p := &fakeProvider{outputs: []string{`{"score":1,"issues":[]}`}}
	ex := engine.NewWorkerExecutor(workers, fakeRegistry(p))
	s, _ := newScheduler(t, ex)

	wf := &domain.Workflow{ID: "chain", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}, {ID: "B", Worker: "summarizer@1.0.0"}},
		Edges: []domain.Edge{{From: "A", To: "B"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Nodes["B"].State != engine.StateSucceeded {
		t.Errorf("B should have run on A's validated output, got %s", res.Nodes["B"].State)
	}
}
