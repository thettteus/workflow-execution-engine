package policy_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/policy"
)

func avail() []policy.Item {
	return []policy.Item{
		{FromNode: "planner", Type: domain.ArtifactMarkdown, Hash: "h-plan", Content: []byte("plan")},
		{FromNode: "differ", Type: domain.ArtifactDiff, Hash: "h-diff", Content: []byte("@@ diff @@")},
	}
}

func nodesOf(items []policy.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.FromNode
	}
	return out
}

func TestResolveDiffOnly(t *testing.T) {
	got, err := policy.Resolve(domain.ContextPolicy{Mode: domain.ContextDiffOnly}, avail())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].FromNode != "differ" {
		t.Errorf("diff-only admitted %v, want only [differ]", nodesOf(got))
	}
}

func TestResolveDefaultIsParentOnly(t *testing.T) {
	// Unset mode must admit the direct parents — never widen, never drop.
	got, err := policy.Resolve(domain.ContextPolicy{}, avail())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("default policy admitted %d items, want 2 (parent-only)", len(got))
	}
}

func TestResolveNoneAdmitsNothing(t *testing.T) {
	got, err := policy.Resolve(domain.ContextPolicy{Mode: domain.ContextNone}, avail())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("none policy admitted %v, want empty", nodesOf(got))
	}
}

func TestResolveArtifactsAllowlist(t *testing.T) {
	p := domain.ContextPolicy{Mode: domain.ContextArtifacts, Params: &domain.ContextPolicyParams{Artifacts: []string{"planner"}}}
	got, err := policy.Resolve(p, avail())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].FromNode != "planner" {
		t.Errorf("artifacts allowlist admitted %v, want only [planner]", nodesOf(got))
	}
}

func TestResolveSummaryUnsupported(t *testing.T) {
	if _, err := policy.Resolve(domain.ContextPolicy{Mode: domain.ContextSummary}, avail()); err == nil {
		t.Error("summary policy should error (deferred), not silently admit full output")
	}
}

func TestHashes(t *testing.T) {
	items := avail()
	hs := policy.Hashes(items)
	if len(hs) != 2 || hs[0] != "h-plan" || hs[1] != "h-diff" {
		t.Errorf("Hashes = %v", hs)
	}
}
