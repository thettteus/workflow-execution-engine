package canonical_test

import (
	"bytes"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
)

func TestMarshalSortsKeysDeterministically(t *testing.T) {
	// Two maps with the same content but different literal orders must produce
	// identical canonical bytes.
	a := map[string]any{"b": 1, "a": 2, "c": map[string]any{"z": 1, "y": 2}}
	b := map[string]any{"c": map[string]any{"y": 2, "z": 1}, "a": 2, "b": 1}

	ab, err := canonical.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := canonical.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ab) != string(bb) {
		t.Fatalf("canonical output differs by key order:\n a: %s\n b: %s", ab, bb)
	}

	const want = `{"a":2,"b":1,"c":{"y":2,"z":1}}`
	if string(ab) != want {
		t.Errorf("unexpected canonical form\n want %s\n  got %s", want, ab)
	}
}

func TestMarshalPreservesNumberPrecision(t *testing.T) {
	got, err := canonical.Marshal(map[string]any{"cost": 0.01, "tokens": 200000})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"cost":0.01,"tokens":200000}`
	if string(got) != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

func TestMarshalNoHTMLEscaping(t *testing.T) {
	got, err := canonical.Marshal(map[string]any{"expr": "a < b && c > d"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"expr":"a < b && c > d"}`
	if string(got) != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

func TestHashStableAndOrderIndependent(t *testing.T) {
	a := map[string]any{"x": 1, "y": 2}
	b := map[string]any{"y": 2, "x": 1}

	ha, err := canonical.Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := canonical.Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("hash depends on key order: %s != %s", ha, hb)
	}
	if len(ha) != 64 {
		t.Errorf("expected 64 hex chars (sha256), got %d", len(ha))
	}
}

// TestWorkflowInputsNilHashesIdenticalToOmitted is REQ-INPUT-01's hash-
// stability guarantee (ADR 0004): every Workflow authored before the Inputs
// field existed must keep hashing identically. A nil slice must marshal to no
// "inputs" key at all, not `"inputs":null` — Go's own encoding/json already
// drops a nil slice under `omitempty`, but this locks the behavior against the
// domain struct itself, not just the tag.
func TestWorkflowInputsNilHashesIdenticalToOmitted(t *testing.T) {
	base := domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes:  []domain.Node{{ID: "a", Worker: "w@1.0.0"}},
		Edges:  []domain.Edge{},
		Budget: domain.Budget{MaxCostUSD: 1},
	}
	withNilInputs := base
	withNilInputs.Inputs = nil

	hBase, err := canonical.Hash(base)
	if err != nil {
		t.Fatal(err)
	}
	hNil, err := canonical.Hash(withNilInputs)
	if err != nil {
		t.Fatal(err)
	}
	if hBase != hNil {
		t.Fatalf("a nil Inputs field changed the hash: %s != %s", hBase, hNil)
	}

	raw, err := canonical.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"inputs"`)) {
		t.Errorf("nil Inputs must produce no \"inputs\" key at all, got: %s", raw)
	}
}
