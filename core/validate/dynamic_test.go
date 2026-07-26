package validate_test

import (
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/validate"
)

// scoreIssuesSchema is the acceptance-test contract shape: {score:number,
// issues:string[]} with both required (REQ-WORKER-03).
func scoreIssuesSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"score", "issues"},
		"properties": map[string]any{
			"score":  map[string]any{"type": "number"},
			"issues": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func TestCompileSchema_ValidPasses(t *testing.T) {
	cs, err := validate.CompileSchema(scoreIssuesSchema())
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if err := cs.ValidateBytes([]byte(`{"score":0.9,"issues":["nit: rename x"]}`)); err != nil {
		t.Errorf("valid output should pass, got: %v", err)
	}
}

func TestCompileSchema_ViolationText(t *testing.T) {
	cs, err := validate.CompileSchema(scoreIssuesSchema())
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	// Wrong type for score, missing issues.
	err = cs.ValidateBytes([]byte(`{"score":"high"}`))
	if err == nil {
		t.Fatal("expected a violation")
	}
	msg := err.Error()
	if !strings.Contains(msg, "score") || !strings.Contains(msg, "issues") {
		t.Errorf("violation text should name the offending fields, got: %s", msg)
	}
}

func TestCompileSchema_NonJSONIsViolation(t *testing.T) {
	cs, err := validate.CompileSchema(scoreIssuesSchema())
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if err := cs.ValidateBytes([]byte("not json at all")); err == nil {
		t.Error("non-JSON output must be reported as a violation")
	}
}
