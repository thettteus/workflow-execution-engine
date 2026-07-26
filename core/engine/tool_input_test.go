package engine

import (
	"os"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

func fixtureInputs() []NodeInput {
	return []NodeInput{
		{FromNode: "fixer", Content: []byte(`{"summary":"fixed the bug","score":9}`), Hash: "h-fixer", Type: domain.ArtifactJSON},
		{FromNode: "diff", Content: []byte(`"@@ -1 +1 @@"`), Hash: "h-diff", Type: domain.ArtifactDiff},
	}
}

// TestResolveToolInputArtifactReference is REQ-WORKER-06's artifact-reference
// half: a whole-string "${nodeID.path}" resolves to the upstream node's field,
// and the referenced hash is recorded for audit (REQ-CTXPOL-03 parity).
func TestResolveToolInputArtifactReference(t *testing.T) {
	secrets := map[string]string{}
	refs := map[string]bool{}
	out, err := resolveToolInput(map[string]any{
		"message": "${fixer.summary}",
		"score":   "${fixer.score}",
		"literal": "not a placeholder",
	}, fixtureInputs(), nil, nil, secrets, refs)
	if err != nil {
		t.Fatalf("resolveToolInput: %v", err)
	}
	m := out.(map[string]any)
	if m["message"] != "fixed the bug" {
		t.Errorf("message = %v, want resolved summary", m["message"])
	}
	if m["score"] != float64(9) {
		t.Errorf("score = %v, want 9 (native JSON number type)", m["score"])
	}
	if m["literal"] != "not a placeholder" {
		t.Errorf("literal string should pass through unchanged, got %v", m["literal"])
	}
	if !refs["h-fixer"] {
		t.Errorf("expected h-fixer to be recorded as referenced, got %v", refs)
	}
	if refs["h-diff"] {
		t.Errorf("h-diff was never referenced, should not be recorded")
	}
}

// TestResolveToolInputEmptyPathIsWholeArtifact confirms "${nodeID}" (no path)
// resolves to that node's entire parsed output.
func TestResolveToolInputEmptyPathIsWholeArtifact(t *testing.T) {
	refs := map[string]bool{}
	out, err := resolveToolInput("${diff}", fixtureInputs(), nil, nil, map[string]string{}, refs)
	if err != nil {
		t.Fatalf("resolveToolInput: %v", err)
	}
	if out != "@@ -1 +1 @@" {
		t.Errorf("out = %v, want the diff node's whole content", out)
	}
}

// TestResolveToolInputEnvReference is REQ-WORKER-06's secret half: "${env:NAME}"
// resolves from the OS environment and is recorded in secrets for redaction.
func TestResolveToolInputEnvReference(t *testing.T) {
	t.Setenv("WEE_TEST_TOKEN", "sk-super-secret")
	secrets := map[string]string{}
	out, err := resolveToolInput(map[string]any{
		"headers": map[string]any{"Authorization": "${env:WEE_TEST_TOKEN}"},
	}, nil, nil, nil, secrets, nil)
	if err != nil {
		t.Fatalf("resolveToolInput: %v", err)
	}
	headers := out.(map[string]any)["headers"].(map[string]any)
	if headers["Authorization"] != "sk-super-secret" {
		t.Errorf("Authorization = %v, want resolved env value", headers["Authorization"])
	}
	if secrets["sk-super-secret"] != "${env:WEE_TEST_TOKEN}" {
		t.Errorf("secret not recorded for redaction: %v", secrets)
	}
}

func TestResolveToolInputMissingEnvErrors(t *testing.T) {
	os.Unsetenv("WEE_TEST_MISSING_VAR")
	_, err := resolveToolInput("${env:WEE_TEST_MISSING_VAR}", nil, nil, nil, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected an error for an unset env var")
	}
}

func TestResolveToolInputOptionalEnvDefaultsToEmpty(t *testing.T) {
	os.Unsetenv("WEE_TEST_OPTIONAL_TOKEN")
	out, err := resolveToolInput("${env:WEE_TEST_OPTIONAL_TOKEN:-}", nil, nil, nil, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("optional env: %v", err)
	}
	if out != "" {
		t.Fatalf("optional env = %q, want empty string", out)
	}
}

func TestResolveToolInputMissingNodeErrors(t *testing.T) {
	_, err := resolveToolInput("${nope.field}", fixtureInputs(), nil, nil, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected an error for a placeholder referencing a non-wired node")
	}
}

func TestResolveToolInputMissingPathErrors(t *testing.T) {
	_, err := resolveToolInput("${fixer.nonexistent}", fixtureInputs(), nil, nil, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected an error for a path absent from the upstream artifact")
	}
}

// TestResolveToolInputWorkflowInputReference is REQ-INPUT-01's placeholder
// half: "${input:NAME}" resolves against the run's resolved workflow inputs,
// distinct from "${env:NAME}" — not recorded in secrets, since it isn't one.
func TestResolveToolInputWorkflowInputReference(t *testing.T) {
	secrets := map[string]string{}
	out, err := resolveToolInput(map[string]any{
		"url": "${input:prUrl}",
	}, nil, map[string]string{"prUrl": "https://api.github.com/repos/acme/widgets/pulls/42"}, nil, secrets, nil)
	if err != nil {
		t.Fatalf("resolveToolInput: %v", err)
	}
	m := out.(map[string]any)
	if m["url"] != "https://api.github.com/repos/acme/widgets/pulls/42" {
		t.Errorf("url = %v, want the resolved input value", m["url"])
	}
	if len(secrets) != 0 {
		t.Errorf("a workflow input is not a secret and must not be recorded for redaction, got %v", secrets)
	}
}

func TestResolveToolInputMissingWorkflowInputErrors(t *testing.T) {
	_, err := resolveToolInput("${input:missing}", nil, map[string]string{}, nil, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected an error for an undeclared/unsupplied workflow input")
	}
}

// TestResolveToolInputNonStringLeavesPassThrough confirms numbers/bools/nil
// are never placeholder candidates.
func TestResolveToolInputNonStringLeavesPassThrough(t *testing.T) {
	out, err := resolveToolInput(map[string]any{
		"n": float64(42), "b": true, "z": nil,
		"list": []any{float64(1), "${fixer.summary}", false},
	}, fixtureInputs(), nil, nil, map[string]string{}, map[string]bool{})
	if err != nil {
		t.Fatalf("resolveToolInput: %v", err)
	}
	m := out.(map[string]any)
	if m["n"] != float64(42) || m["b"] != true || m["z"] != nil {
		t.Errorf("non-string leaves altered: %+v", m)
	}
	list := m["list"].([]any)
	if list[0] != float64(1) || list[1] != "fixed the bug" || list[2] != false {
		t.Errorf("array recursion incorrect: %+v", list)
	}
}

func TestRedactBytesAndString(t *testing.T) {
	secrets := map[string]string{"sk-super-secret": "${env:WEE_TEST_TOKEN}"}
	b := redactBytes([]byte(`{"header":"Bearer sk-super-secret"}`), secrets)
	if got := string(b); got != `{"header":"Bearer ${env:WEE_TEST_TOKEN}"}` {
		t.Errorf("redactBytes = %s", got)
	}
	s := redactString("request failed: token sk-super-secret rejected", secrets)
	if s != "request failed: token ${env:WEE_TEST_TOKEN} rejected" {
		t.Errorf("redactString = %s", s)
	}
}
