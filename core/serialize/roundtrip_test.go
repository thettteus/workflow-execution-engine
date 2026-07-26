package serialize_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
)

// roundTrip loads a YAML fixture into T, then checks two invariants:
//   - YAML → T → YAML → T is loss-free
//   - YAML → T → JSON → T is loss-free (formats are interchangeable)
//
// Canonical-hash equality is the oracle for "identical": two values hash the
// same iff their content is identical, and it sidesteps time.Time / free-form
// map comparison quirks that reflect.DeepEqual is fragile about.
func roundTrip[T any](t *testing.T, path string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var a T
	if err := serialize.UnmarshalYAML(data, &a); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	want, err := canonical.Hash(a)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	// YAML round-trip.
	yb, err := serialize.MarshalYAML(a)
	if err != nil {
		t.Fatalf("marshal YAML: %v", err)
	}
	var b T
	if err := serialize.UnmarshalYAML(yb, &b); err != nil {
		t.Fatalf("re-unmarshal YAML: %v", err)
	}
	if got, _ := canonical.Hash(b); got != want {
		t.Errorf("YAML round-trip not loss-free\n want %s\n  got %s", want, got)
	}

	// Cross-format round-trip.
	jb, err := serialize.MarshalJSON(a)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var c T
	if err := serialize.UnmarshalJSON(jb, &c); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	if got, _ := canonical.Hash(c); got != want {
		t.Errorf("YAML→JSON round-trip not loss-free\n want %s\n  got %s", want, got)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := "testdata"
	t.Run("workflow", func(t *testing.T) { roundTrip[domain.Workflow](t, filepath.Join(dir, "workflow.yaml")) })
	t.Run("worker", func(t *testing.T) { roundTrip[domain.Worker](t, filepath.Join(dir, "worker.yaml")) })
	t.Run("contract", func(t *testing.T) { roundTrip[domain.Contract](t, filepath.Join(dir, "contract.yaml")) })
	t.Run("context-policy", func(t *testing.T) { roundTrip[domain.ContextPolicy](t, filepath.Join(dir, "context-policy.yaml")) })
	t.Run("artifact", func(t *testing.T) { roundTrip[domain.Artifact](t, filepath.Join(dir, "artifact.yaml")) })
	t.Run("event", func(t *testing.T) { roundTrip[domain.Event](t, filepath.Join(dir, "event.yaml")) })
	t.Run("execution", func(t *testing.T) { roundTrip[domain.Execution](t, filepath.Join(dir, "execution.yaml")) })
	t.Run("budget", func(t *testing.T) { roundTrip[domain.Budget](t, filepath.Join(dir, "budget.yaml")) })
}

// TestLoadWorkflow exercises the headline load API and confirms the two file
// formats produce byte-identical canonical content.
func TestLoadWorkflowFormatsAgree(t *testing.T) {
	wf, err := serialize.LoadWorkflow(filepath.Join("testdata", "workflow.yaml"))
	if err != nil {
		t.Fatalf("LoadWorkflow(yaml): %v", err)
	}

	// Save as JSON, reload, and confirm the canonical hash is unchanged.
	jsonPath := filepath.Join(t.TempDir(), "workflow.json")
	if err := serialize.SaveWorkflow(wf, jsonPath); err != nil {
		t.Fatalf("SaveWorkflow(json): %v", err)
	}
	wf2, err := serialize.LoadWorkflow(jsonPath)
	if err != nil {
		t.Fatalf("LoadWorkflow(json): %v", err)
	}

	h1, _ := canonical.Hash(wf)
	h2, _ := canonical.Hash(wf2)
	if h1 != h2 {
		t.Errorf("YAML and JSON forms disagree:\n yaml %s\n json %s", h1, h2)
	}
}
