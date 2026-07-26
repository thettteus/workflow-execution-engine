package contract

import (
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/validate"
)

// ViolationError means a Worker's output failed its Contract's output schema.
// Feedback is the validation errors verbatim — the delta appended to the next
// attempt's messages (PRIN-05). The engine maps this to its contract-violation
// retry class; other errors from Enforce (e.g. an invalid schema) are fatal
// configuration faults, not violations.
type ViolationError struct {
	Feedback string
	err      error
}

func (e *ViolationError) Error() string { return e.err.Error() }
func (e *ViolationError) Unwrap() error { return e.err }

// Enforce runs the output pipeline for one Worker attempt (REQ-CONTRACT-01..03):
// parse the output as JSON and validate it against the Contract's outputSchema.
// It returns:
//   - nil                       — output conforms; safe to propagate downstream;
//   - *ViolationError           — output is not JSON or violates the schema
//     (retryable with delta feedback);
//   - any other error           — the Contract's outputSchema itself is invalid
//     (a fatal configuration fault).
//
// It never mutates or "repairs" output: a Contract is enforcement, not
// suggestion — malformed output is rejected, never silently passed through
// (REQ-WORKER-03).
func Enforce(c domain.Contract, output []byte) error {
	cs, err := validate.CompileSchema(c.OutputSchema)
	if err != nil {
		return fmt.Errorf("contract: invalid outputSchema: %w", err)
	}
	if verr := cs.ValidateBytes(output); verr != nil {
		return &ViolationError{Feedback: verr.Error(), err: verr}
	}
	return nil
}
