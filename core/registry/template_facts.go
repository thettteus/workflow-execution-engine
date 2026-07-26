package registry

import (
	"sort"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// TemplateFacts are presentation-layer facts derived from a canonical
// Workflow — the numbers and safety declaration a gallery card, a `wee
// export` summary, or the published-catalog test want, without re-deriving
// them ad hoc in each place. WriteCapable is a provisional, presentation-layer
// classification for template curation (M2.3) — it is not the canonical
// "what counts as a mutation" taxonomy; M2.5's charter is to define that via
// ADR and may supersede this. JSON tags let callers (core/server's Template
// API row) serialize these fields directly, without a duplicate DTO.
type TemplateFacts struct {
	Tools              []string        `json:"tools"`
	WriteCapable       bool            `json:"writeCapable"`
	ExpectedCostUsd    float64         `json:"expectedCostUsd"`
	ExpectedDurationMs int64           `json:"expectedDurationMs"`
	Inputs             []TemplateInput `json:"inputs"`
}

// TemplateInput mirrors one domain.InputDecl — enough for a gallery or CLI to
// guide a user before they run the template.
type TemplateInput struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Default     string `json:"default"`
}

// DeriveTemplateFacts computes TemplateFacts for wf. It needs no Registry
// state — a pure function of the canonical Workflow. Tools/Inputs are always
// non-nil (empty rather than nil when there are none): both travel over the
// wire as a JSON array, and a nil Go slice marshals to `null`, not `[]` — a
// client typed against a non-nullable array (ui/src/core/audit.ts's
// Template) would crash calling .length on it (caught live: refactor-plan,
// which declares no workflow inputs, round-tripped as "inputs": null through
// the real GET /api/templates before this fix).
func DeriveTemplateFacts(wf domain.Workflow) TemplateFacts {
	facts := TemplateFacts{
		Tools:              []string{},
		ExpectedCostUsd:    wf.Budget.MaxCostUSD,
		ExpectedDurationMs: wf.Budget.MaxDurationMs,
		Inputs:             []TemplateInput{},
	}

	seen := make(map[string]bool)
	for _, n := range wf.Nodes {
		if n.Tool == nil {
			continue
		}
		if !seen[n.Tool.ToolName] {
			seen[n.Tool.ToolName] = true
			facts.Tools = append(facts.Tools, n.Tool.ToolName)
		}
		if writeCapable(n.Tool) {
			facts.WriteCapable = true
		}
	}
	sort.Strings(facts.Tools)

	for _, in := range wf.Inputs {
		facts.Inputs = append(facts.Inputs, TemplateInput{
			Name:        in.Name,
			Required:    in.Required,
			Description: in.Description,
			Default:     in.Default,
		})
	}

	return facts
}

// writeCapable classifies one tool call by an allowlist of known-safe
// literal ops, not a denylist of known-unsafe ones. This polarity is
// deliberate: a tool input leaf may legally be an unresolved
// "${input:NAME}"/"${nodeID.path}" placeholder, only resolved at run time
// (core/engine/tool_input.go's resolveToolInput walks every string leaf
// generically, with no field-name exception). A denylist would let such a
// placeholder — or any op this function doesn't yet know about — silently
// default to "read-only." Anything that isn't an exact match against the
// known-safe literal set counts as write-capable instead (deny-first,
// PRIN-10).
func writeCapable(t *domain.ToolCall) bool {
	switch t.ToolName {
	case "git":
		op, _ := t.Input["op"].(string)
		return !(op == "status" || op == "diff")
	case "filesystem":
		op, _ := t.Input["op"].(string)
		return !(op == "read" || op == "list")
	case "http":
		method, _ := t.Input["method"].(string)
		// GET is the only safe literal. POST always counts as write-capable
		// here even though some public APIs (e.g. batch-query endpoints)
		// use POST for a read — erring toward "declares write" is the safe
		// direction for a curation gate; a workflow author who wants such a
		// template gallery-published can say so explicitly once a real
		// mutation taxonomy exists (M2.5).
		return method != "GET"
	case "terminal":
		// An arbitrary allowlisted command — Core cannot structurally prove
		// any invocation is side-effect-free.
		return true
	default:
		// An unknown tool name — deny-first.
		return true
	}
}
