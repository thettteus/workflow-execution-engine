// Package contract compiles a Worker + its resolved context into model messages
// and enforces the Worker's output against its Contract's schema. A Contract is
// enforced, not suggested (spec/contracts.md): output that fails its schema
// never reaches a downstream node.
//
// Message construction (compiler.go) is internal plumbing — the one and only
// place in the codebase model-input text is built — and is never surfaced as a
// user-facing concept (PRIN-04).
package contract

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/policy"
)

// Compile turns a Worker, its policy-resolved context, and optional retry
// feedback into the ordered messages for one model call (REQ-CONTRACT-01
// plumbing, REQ-CTXPOL-01). It is deterministic and side-effect free.
//
// The system message states the role and the enforced contract (including the
// required output schema); one user message carries exactly the admitted context
// items. On a contract-violation retry, feedback is the validation errors and
// only the errors — appended as a short corrective message, never a re-inflated
// copy of the context (PRIN-05, delta feedback).
func Compile(w domain.Worker, context []policy.Item, feedback string) []model.Message {
	msgs := []model.Message{{Role: model.RoleSystem, Content: buildSystem(w)}}

	if len(context) > 0 {
		msgs = append(msgs, model.Message{Role: model.RoleUser, Content: buildContext(context)})
	}

	if feedback != "" {
		var b strings.Builder
		b.WriteString("Your previous output did not satisfy the required schema. ")
		b.WriteString("Fix only these problems and return the full corrected JSON object:\n")
		b.WriteString(feedback)
		msgs = append(msgs, model.Message{Role: model.RoleUser, Content: b.String()})
	}

	return msgs
}

func buildSystem(w domain.Worker) string {
	var b strings.Builder
	if w.Objective != "" {
		fmt.Fprintf(&b, "Objective: %s\n", w.Objective)
	}
	writeBullets(&b, "Constraints", w.Constraints)

	c := w.Contract
	if c.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", c.Goal)
	}
	writeBullets(&b, "Rules", c.Rules)
	writeBullets(&b, "Success criteria", c.SuccessCriteria)

	b.WriteString("\nRespond with a single JSON object and nothing else. ")
	b.WriteString("It must conform exactly to this JSON Schema:\n")
	if schema, err := json.MarshalIndent(c.OutputSchema, "", "  "); err == nil {
		b.Write(schema)
	}
	return b.String()
}

func buildContext(items []policy.Item) string {
	var b strings.Builder
	b.WriteString("Context (exactly the artifacts your policy admits):\n")
	for _, it := range items {
		fmt.Fprintf(&b, "\n--- from %q (%s) ---\n", it.FromNode, it.Type)
		b.Write(it.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func writeBullets(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, it := range items {
		fmt.Fprintf(b, "  - %s\n", it)
	}
}
