// Package policy resolves a Worker's declared context policy into exactly the
// slice of upstream artifacts the Worker is allowed to see — nothing more
// (REQ-CTXPOL-01). This is the token-economy principle (PRIN-05) made
// mechanical: a reviewer scoped to diff-only cannot be bloated, or biased, by a
// sibling's output. The package is intentionally free of engine, model, and
// transport dependencies so the resolver stays a pure function of policy +
// available artifacts, independently testable.
//
// It is named "policy" rather than "context" to avoid colliding with the stdlib
// context package.
package policy

import (
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// Item is one upstream artifact available to a node: the producing node's id,
// its artifact type, its content hash (for audit — REQ-CTXPOL-03), and the
// content itself. The engine builds these from a node's active incoming edges.
type Item struct {
	FromNode string
	Type     domain.ArtifactType
	Hash     string
	Content  []byte
}

// Resolve returns the subset of available items the policy admits, in input
// order. It never adds anything not present in available, and an unset policy
// mode (or ContextParentOnly) admits the direct parents as-is — the smallest
// slice that satisfies the contract, never "full history" (REQ-CTXPOL-02).
//
// The caller is responsible for logging the admitted hashes so what a Worker saw
// is auditable later (REQ-CTXPOL-03).
func Resolve(p domain.ContextPolicy, available []Item) ([]Item, error) {
	switch p.Mode {
	case "", domain.ContextParentOnly, domain.ContextFull:
		// available already IS the set of direct-parent outputs carried by the
		// node's active edges. Parent-only admits them all; "full" is the same
		// today (the engine surfaces direct parents at this seam) and widens no
		// further without an explicit artifacts policy.
		return clone(available), nil

	case domain.ContextNone:
		return nil, nil

	case domain.ContextDiffOnly:
		return filter(available, func(it Item) bool { return it.Type == domain.ArtifactDiff }), nil

	case domain.ContextArtifacts:
		allowed := map[string]bool{}
		if p.Params != nil {
			for _, id := range p.Params.Artifacts {
				allowed[id] = true
			}
		}
		return filter(available, func(it Item) bool { return allowed[it.FromNode] }), nil

	case domain.ContextSummary:
		// A summary policy needs a summarization step that does not exist yet;
		// admitting the full parent output would silently violate the policy, so
		// we refuse rather than mislead (PRIN honesty). Deferred beyond M1.4.
		return nil, fmt.Errorf("policy: context mode %q is not yet supported", p.Mode)

	default:
		return nil, fmt.Errorf("policy: unknown context mode %q", p.Mode)
	}
}

// Hashes returns the content hashes of the admitted items, for the audit record
// (REQ-CTXPOL-03).
func Hashes(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Hash)
	}
	return out
}

func filter(items []Item, keep func(Item) bool) []Item {
	var out []Item
	for _, it := range items {
		if keep(it) {
			out = append(out, it)
		}
	}
	return out
}

func clone(items []Item) []Item {
	if items == nil {
		return nil
	}
	out := make([]Item, len(items))
	copy(out, items)
	return out
}
