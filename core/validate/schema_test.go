package validate_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/core/validate"
)

func TestNewValidatorCompilesAllSchemas(t *testing.T) {
	if _, err := validate.NewValidator(); err != nil {
		t.Fatalf("NewValidator (cross-file $refs must resolve): %v", err)
	}
}

func TestValidateAcceptsValidWorkflow(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	wf, err := serialize.LoadWorkflow(filepath.Join("..", "serialize", "testdata", "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("valid workflow rejected:\n%v", err)
	}
}

func TestValidateReportsPositionalError(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "bad-enum.yaml")
	wf, err := serialize.LoadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := serialize.LoadSource(path)
	if err != nil {
		t.Fatal(err)
	}

	err = v.Validate(validate.KindWorkflow, wf, src)
	if err == nil {
		t.Fatal("expected schema validation to fail on an out-of-enum context mode")
	}

	var se *validate.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("expected *validate.SchemaError, got %T", err)
	}

	msg := err.Error()
	// The offending value is on line 7 of the fixture, at the mode field.
	if !strings.Contains(msg, "line 7") {
		t.Errorf("error should cite the source line (7):\n%s", msg)
	}
	if !strings.Contains(msg, "mode") {
		t.Errorf("error should point at the mode field:\n%s", msg)
	}
	if !strings.Contains(msg, "bad-enum.yaml") {
		t.Errorf("error should name the source file:\n%s", msg)
	}
}
