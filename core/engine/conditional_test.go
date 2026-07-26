package engine

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

func TestEvalCondition(t *testing.T) {
	doc := []byte(`{"score": 8, "passed": true, "label": "ok", "result": {"errors": 0}, "items": ["a", "b"]}`)

	cases := []struct {
		name string
		cond *domain.Condition
		json []byte
		want bool
	}{
		{"nil is unconditional", nil, doc, true},
		{"eq number true", &domain.Condition{Path: "score", Op: domain.OpEq, Value: 8}, doc, true},
		{"eq number false", &domain.Condition{Path: "score", Op: domain.OpEq, Value: 9}, doc, false},
		{"ne number", &domain.Condition{Path: "score", Op: domain.OpNe, Value: 9}, doc, true},
		{"gte boundary", &domain.Condition{Path: "score", Op: domain.OpGte, Value: 8}, doc, true},
		{"gt boundary false", &domain.Condition{Path: "score", Op: domain.OpGt, Value: 8}, doc, false},
		{"lt true", &domain.Condition{Path: "score", Op: domain.OpLt, Value: 10}, doc, true},
		{"nested path lte", &domain.Condition{Path: "result.errors", Op: domain.OpLte, Value: 0}, doc, true},
		{"string eq", &domain.Condition{Path: "label", Op: domain.OpEq, Value: "ok"}, doc, true},
		{"bool truthy", &domain.Condition{Path: "passed", Op: domain.OpTruthy}, doc, true},
		{"exists true", &domain.Condition{Path: "label", Op: domain.OpExists}, doc, true},
		{"exists false", &domain.Condition{Path: "missing", Op: domain.OpExists}, doc, false},
		{"array index", &domain.Condition{Path: "items.1", Op: domain.OpEq, Value: "b"}, doc, true},
		{"missing path ne is true", &domain.Condition{Path: "missing", Op: domain.OpNe, Value: 1}, doc, true},
		{"missing path eq is false", &domain.Condition{Path: "missing", Op: domain.OpEq, Value: 1}, doc, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evalCondition(tc.cond, tc.json)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("evalCondition = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvalConditionErrors(t *testing.T) {
	// Non-JSON upstream content with a condition is an error.
	if _, err := evalCondition(&domain.Condition{Path: "x", Op: domain.OpEq, Value: 1}, []byte("not json")); err == nil {
		t.Error("expected error for non-JSON artifact")
	}
	// Ordering operator on a non-numeric value is an error.
	if _, err := evalCondition(&domain.Condition{Path: "label", Op: domain.OpGt, Value: 1}, []byte(`{"label":"x"}`)); err == nil {
		t.Error("expected error comparing a string with >")
	}
}
