package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/validate"
	"github.com/thettteus/workflow-execution-engine/schemas"
)

// drift compares the top-level property names declared by a schema against the
// top-level keys a fully-populated struct serializes to. A mismatch in either
// direction is drift: a Go field with no schema counterpart, or vice versa.
// The instance is also validated against the schema (which, with
// additionalProperties:false, independently catches unknown struct fields).
func TestSchemaDrift(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	fin := ts.Add(9 * time.Second)

	budget := domain.Budget{MaxCostUSD: 1.5, MaxTokens: 200000, MaxDurationMs: 600000, MaxRetriesPerNode: 2}
	policy := domain.ContextPolicy{Mode: domain.ContextArtifacts, Params: &domain.ContextPolicyParams{Artifacts: []string{"a"}}}
	contract := domain.Contract{
		Goal:            "produce a review",
		Rules:           []string{"cite lines"},
		OutputSchema:    map[string]any{"type": "object"},
		SuccessCriteria: []string{"no critical miss"},
		MaxRetries:      2,
	}
	model := domain.ModelConfig{Provider: "anthropic", Model: "claude-sonnet-5", Params: map[string]any{"temperature": 0}}
	worker := domain.Worker{
		ID: "reviewer", Version: "1.0.0", Description: "Reviews a change for correctness.", Objective: "review",
		Constraints: []string{"diff only"}, Tools: []string{"git"},
		ContextPolicy: domain.ContextPolicy{Mode: domain.ContextDiffOnly}, Contract: contract, Model: model,
	}
	workflow := domain.Workflow{
		ID: "pr-review", Version: "1.0.0",
		Nodes:    []domain.Node{{ID: "a", Worker: "reviewer@1.0.0", ContextPolicy: &policy}},
		Edges:    []domain.Edge{{From: "a", To: "a"}},
		Defaults: &domain.Defaults{Model: &model, ContextPolicy: &domain.ContextPolicy{Mode: domain.ContextParentOnly}},
		Inputs:   []domain.InputDecl{{Name: "prUrl", Required: true, Description: "PR diff URL to review"}},
		Budget:   budget,
	}

	cases := []struct {
		kind     validate.Kind
		file     string
		instance any
	}{
		{validate.KindBudget, "budget.schema.json", budget},
		{validate.KindContextPolicy, "context-policy.schema.json", policy},
		{validate.KindContract, "contract.schema.json", contract},
		{validate.KindArtifact, "artifact.schema.json", domain.Artifact{
			ID: "art-1", Type: "report", ContentHash: "abc", MimeType: "text/markdown",
			Metadata: map[string]any{"k": "v"}, ProducedBy: "a",
		}},
		{validate.KindEvent, "event.schema.json", domain.Event{
			Type: "WorkerFinished", Timestamp: ts, ExecutionID: "e1", NodeID: "a",
			Payload: map[string]any{"k": "v"},
		}},
		{validate.KindWorker, "worker.schema.json", worker},
		{validate.KindWorkflow, "workflow.schema.json", workflow},
		{validate.KindExecution, "execution.schema.json", domain.Execution{
			ID: "e1", WorkflowRef: "pr-review@1.0.0", State: domain.ExecutionSucceeded,
			Graph:      workflow,
			Budget:     domain.BudgetStatus{Limit: budget, SpentCostUSD: 0.4, SpentTokens: 51234, ElapsedMs: 8300},
			StartedAt:  ts,
			FinishedAt: &fin,
		}},
	}

	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			// The populated instance must satisfy its schema.
			if err := v.Validate(tc.kind, tc.instance, nil); err != nil {
				t.Fatalf("populated %s instance failed its own schema:\n%v", tc.kind, err)
			}

			schemaProps := topLevelSchemaProps(t, tc.file)
			structKeys := topLevelStructKeys(t, tc.instance)

			for prop := range schemaProps {
				if !structKeys[prop] {
					t.Errorf("schema %s declares property %q with no populated struct field (drift)", tc.file, prop)
				}
			}
			for key := range structKeys {
				if !schemaProps[key] {
					t.Errorf("struct for %s serializes field %q absent from schema %s (drift)", tc.kind, key, tc.file)
				}
			}
		})
	}
}

func topLevelSchemaProps(t *testing.T, file string) map[string]bool {
	t.Helper()
	data, err := schemas.FS.ReadFile(file)
	if err != nil {
		t.Fatalf("read schema %s: %v", file, err)
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema %s: %v", file, err)
	}
	out := make(map[string]bool, len(doc.Properties))
	for k := range doc.Properties {
		out[k] = true
	}
	return out
}

func topLevelStructKeys(t *testing.T, instance any) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(instance)
	if err != nil {
		t.Fatalf("marshal instance: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
