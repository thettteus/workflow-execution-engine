package registry_test

import (
	"bytes"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/registry"
)

// TestExportImportRoundTripsIdenticalHash is the REQ-VERSION-03 / REQ-DEF-02
// acceptance path: a workflow and the worker it references, exported and then
// imported into a fresh registry, come back with byte-identical content hashes.
func TestExportImportRoundTripsIdenticalHash(t *testing.T) {
	src := registry.New()
	if err := src.RegisterWorker(worker("rev", "1.0.0", "review code")); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	wf := domain.Workflow{
		ID: "pr-flow", Version: "1.2.0",
		Nodes: []domain.Node{{ID: "review", Worker: "rev@1.0.0"}},
	}
	if err := src.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow: %v", err)
	}

	archive, err := src.Export("pr-flow", "1.2.0")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst, err := registry.Import(archive)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	for _, ref := range []string{"pr-flow@1.2.0", "rev@1.0.0"} {
		want, ok := src.ContentHash(ref)
		if !ok {
			t.Fatalf("source registry lost %q", ref)
		}
		got, ok := dst.ContentHash(ref)
		if !ok {
			t.Errorf("imported registry is missing %q", ref)
			continue
		}
		if got != want {
			t.Errorf("content hash for %q changed across round-trip: export=%q import=%q", ref, want, got)
		}
	}

	// The imported workflow must resolve its worker just like the source.
	if _, ok := dst.Lookup("rev@1.0.0"); !ok {
		t.Error("imported registry cannot resolve the bundled worker")
	}
}

// TestExportExcludesResolvedSecretsPreservesReferences locks NFR-SEC-01 for the
// export path: a definition carries only secret *references* (`${env:NAME}`),
// never resolved values, so even with a real secret value present in the
// environment the archive contains the reference (portability) but never the
// value.
func TestExportExcludesResolvedSecretsPreservesReferences(t *testing.T) {
	const secretValue = "ghp_SUPERSECRETVALUE_should_never_be_exported"
	t.Setenv("MY_TEST_SECRET", secretValue)

	reg := registry.New()
	wf := domain.Workflow{
		ID: "gh-flow", Version: "1.0.0",
		Nodes: []domain.Node{{
			ID: "call",
			Tool: &domain.ToolCall{
				ToolName: "http",
				Input: map[string]any{
					"method": "GET",
					"url":    "https://api.example.com",
					"headers": map[string]any{
						"Authorization": "${env:MY_TEST_SECRET}",
					},
				},
			},
		}},
	}
	if err := reg.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow: %v", err)
	}

	archive, err := reg.Export("gh-flow", "1.0.0")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if bytes.Contains(archive, []byte(secretValue)) {
		t.Error("resolved secret value leaked into the export archive (NFR-SEC-01 violation)")
	}
	if !bytes.Contains(archive, []byte("${env:MY_TEST_SECRET}")) {
		t.Error("secret reference was stripped from the archive — the bundle is no longer portable")
	}
}

// TestExportUnregisteredWorkflowErrors: exporting a name@version that was never
// registered is a clear error, not an empty archive.
func TestExportUnregisteredWorkflowErrors(t *testing.T) {
	reg := registry.New()
	if _, err := reg.Export("nope", "1.0.0"); err == nil {
		t.Error("exporting an unregistered workflow should error")
	}
}

// TestExportPartialBundleErrors: a workflow referencing a worker that isn't
// registered cannot be exported as a partial bundle.
func TestExportPartialBundleErrors(t *testing.T) {
	reg := registry.New()
	wf := domain.Workflow{
		ID: "wf", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "a", Worker: "missing@1.0.0"}},
	}
	if err := reg.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow: %v", err)
	}
	if _, err := reg.Export("wf", "1.0.0"); err == nil {
		t.Error("exporting a workflow with an unregistered worker should error")
	}
}
