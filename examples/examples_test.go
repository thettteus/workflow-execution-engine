package examples_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzpereira/workflow-execution-engine/core/domain"
	"github.com/tzpereira/workflow-execution-engine/core/registry"
	"github.com/tzpereira/workflow-execution-engine/core/serialize"
	"github.com/tzpereira/workflow-execution-engine/core/validate"
)

// TestExamplesAreValid loads every shipped example and validates it against the
// domain schemas, so an example can never drift from the model (REQ-CONTRACT-04
// verification: the shipped templates are real, well-formed definitions).
func TestExamplesAreValid(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	workflow := readWorkflow(t, "pr-review/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, workflow, nil); err != nil {
		t.Errorf("workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&workflow, nil); err != nil {
		t.Errorf("workflow.yaml failed graph validation:\n%v", err)
	}
	if len(workflow.Nodes) != 2 || workflow.Nodes[0].Tool == nil || workflow.Nodes[1].Worker == "" {
		t.Error("pr-review should stay a two-node, one-model-call remote review path")
	}
	for _, node := range workflow.Nodes {
		if node.Tool != nil && node.Tool.ToolName != "http" {
			t.Errorf("pr-review must be read-only; found tool %q", node.Tool.ToolName)
		}
	}

	worker := readWorker(t, "pr-review/reviewer.worker.yaml")
	if err := v.Validate(validate.KindWorker, worker, nil); err != nil {
		t.Errorf("reviewer.worker.yaml failed schema validation:\n%v", err)
	}

	// The Worker's output contract must be a compilable JSON Schema.
	if _, err := validate.CompileSchema(worker.Contract.OutputSchema); err != nil {
		t.Errorf("reviewer output schema does not compile: %v", err)
	}

	ghWorkflow := readWorkflow(t, "github-pr-review/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, ghWorkflow, nil); err != nil {
		t.Errorf("github-pr-review/workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&ghWorkflow, nil); err != nil {
		t.Errorf("github-pr-review/workflow.yaml failed graph validation:\n%v", err)
	}
	for _, n := range ghWorkflow.Nodes {
		if n.Tool == nil {
			t.Errorf("github-pr-review node %q should be tool-backed", n.ID)
		}
	}
}

// TestRefactorPlanIsReadOnlyLocalGitDiffShape locks in M2.3's non-GitHub
// change-source shape: refactor-plan mirrors pr-review's two-node,
// one-model-call shape exactly, but node 1 is a read-only `git diff` instead
// of `http` — proof the change source is workflow configuration, not Core.
func TestRefactorPlanIsReadOnlyLocalGitDiffShape(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	wf := readWorkflow(t, "refactor-plan/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&wf, nil); err != nil {
		t.Errorf("workflow.yaml failed graph validation:\n%v", err)
	}
	if len(wf.Nodes) != 2 || wf.Nodes[0].Tool == nil || wf.Nodes[1].Worker == "" {
		t.Fatal("refactor-plan should stay a two-node, one-model-call local-diff path (same shape as pr-review)")
	}
	if wf.Nodes[0].Tool.ToolName != "git" {
		t.Errorf("fetch node tool = %q, want git (the local-diff change source)", wf.Nodes[0].Tool.ToolName)
	}
	if op, _ := wf.Nodes[0].Tool.Input["op"].(string); op != "diff" {
		t.Errorf("fetch node git op = %q, want diff (read-only)", op)
	}
	if facts := registry.DeriveTemplateFacts(wf); facts.WriteCapable {
		t.Error("refactor-plan must be read-only (WriteCapable == false) to be gallery-eligible without the M2.5 approval gate")
	}

	worker := readWorker(t, "refactor-plan/plan.worker.yaml")
	if err := v.Validate(validate.KindWorker, worker, nil); err != nil {
		t.Errorf("plan.worker.yaml failed schema validation:\n%v", err)
	}
	if _, err := validate.CompileSchema(worker.Contract.OutputSchema); err != nil {
		t.Errorf("plan output schema does not compile: %v", err)
	}
}

// TestReleaseNotesIsReadOnlyGenericPatchURLShape locks in M2.3's generic
// public-patch/diff-URL change-source shape: a plain http GET with no
// urlRewrite and no GitHub-API media type — distinct from pr-review/
// change-risk's api.github.com JSON dance, and gallery-eligible (read-only)
// without waiting on M2.5.
func TestReleaseNotesIsReadOnlyGenericPatchURLShape(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	wf := readWorkflow(t, "release-notes/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&wf, nil); err != nil {
		t.Errorf("workflow.yaml failed graph validation:\n%v", err)
	}
	if len(wf.Nodes) != 2 || wf.Nodes[0].Tool == nil || wf.Nodes[1].Worker == "" {
		t.Fatal("release-notes should stay a two-node, one-model-call patch-URL path")
	}
	if wf.Nodes[0].Tool.ToolName != "http" {
		t.Errorf("fetch node tool = %q, want http", wf.Nodes[0].Tool.ToolName)
	}
	if _, hasRewrite := wf.Nodes[0].Tool.Input["urlRewrite"]; hasRewrite {
		t.Error("release-notes should need no urlRewrite — that's the point of the generic patch-URL shape")
	}
	if method, _ := wf.Nodes[0].Tool.Input["method"].(string); method != "GET" {
		t.Errorf("fetch node http method = %q, want GET (read-only)", method)
	}
	if facts := registry.DeriveTemplateFacts(wf); facts.WriteCapable {
		t.Error("release-notes must be read-only (WriteCapable == false) to be gallery-eligible without the M2.5 approval gate")
	}

	worker := readWorker(t, "release-notes/notes.worker.yaml")
	if err := v.Validate(validate.KindWorker, worker, nil); err != nil {
		t.Errorf("notes.worker.yaml failed schema validation:\n%v", err)
	}
	if _, err := validate.CompileSchema(worker.Contract.OutputSchema); err != nil {
		t.Errorf("notes output schema does not compile: %v", err)
	}
}

// TestDependencyAuditIsReadOnlyLocalFileShape locks in M2.3's local-file
// change-source shape: a read-only filesystem read, no network anywhere in
// the graph — the only non-HTTP, non-git shape in this gallery — and
// gallery-eligible (read-only) without waiting on M2.5.
func TestDependencyAuditIsReadOnlyLocalFileShape(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	wf := readWorkflow(t, "dependency-audit/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&wf, nil); err != nil {
		t.Errorf("workflow.yaml failed graph validation:\n%v", err)
	}
	if len(wf.Nodes) != 2 || wf.Nodes[0].Tool == nil || wf.Nodes[1].Worker == "" {
		t.Fatal("dependency-audit should stay a two-node, one-model-call local-file path")
	}
	if wf.Nodes[0].Tool.ToolName != "filesystem" {
		t.Errorf("read node tool = %q, want filesystem", wf.Nodes[0].Tool.ToolName)
	}
	if op, _ := wf.Nodes[0].Tool.Input["op"].(string); op != "read" {
		t.Errorf("read node filesystem op = %q, want read (read-only)", op)
	}
	if facts := registry.DeriveTemplateFacts(wf); facts.WriteCapable {
		t.Error("dependency-audit must be read-only (WriteCapable == false) to be gallery-eligible without the M2.5 approval gate")
	}

	worker := readWorker(t, "dependency-audit/audit.worker.yaml")
	if err := v.Validate(validate.KindWorker, worker, nil); err != nil {
		t.Errorf("audit.worker.yaml failed schema validation:\n%v", err)
	}
	if _, err := validate.CompileSchema(worker.Contract.OutputSchema); err != nil {
		t.Errorf("audit output schema does not compile: %v", err)
	}
}

// TestPublishedTemplateCatalogDeclaresMutationSafety keeps M2.3's structural
// safety declaration while allowing M2.12's one deliberate write-capable
// flagship after M2.5 delivered persistent approval checkpoints. Every other
// gallery template remains read-only; the exception is explicit and tested as
// write-capable so the UI cannot lose its warning.
func TestPublishedTemplateCatalogDeclaresMutationSafety(t *testing.T) {
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		found++
		data, err := os.ReadFile(filepath.Join("templates", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		reg, err := registry.Import(data)
		if err != nil {
			t.Fatalf("import %s: %v", entry.Name(), err)
		}
		_, wf, ok := reg.SoleWorkflow()
		if !ok {
			t.Fatalf("%s: expected exactly one workflow", entry.Name())
		}
		facts := registry.DeriveTemplateFacts(wf)
		if wf.ID == "pr-review-autofix" {
			if !facts.WriteCapable {
				t.Errorf("%s must remain classified write-capable so the gallery shows its approval warning", entry.Name())
			}
			if len(facts.Inputs) != 1 || facts.Inputs[0].Name != "prUrl" || !facts.Inputs[0].Required {
				t.Errorf("%s required input hints = %+v, want required prUrl", entry.Name(), facts.Inputs)
			}
			workers := reg.Workers(wf)
			if len(workers) != 7 {
				t.Errorf("%s bundled Workers = %d, want 7", entry.Name(), len(workers))
			}
			for ref, worker := range workers {
				if worker.Description == "" {
					t.Errorf("%s bundled Worker %s lost its description", entry.Name(), ref)
				}
			}
			continue
		}
		if facts.WriteCapable {
			t.Errorf("%s (%s) is unexpectedly write-capable (tools=%v); only pr-review-autofix is approved for the guarded gallery path", entry.Name(), wf.ID, facts.Tools)
		}
	}
	if found == 0 {
		t.Fatal("no .tar files found in examples/templates")
	}
}

// TestFlagshipIsValid is the M1.14/M1.15 flagship (pulled forward so the
// Template gallery has a real bundle) — same schema/graph validation as the
// other examples, plus a check that the parallel-reviewer shape VISION.md
// describes is actually present.
func TestFlagshipIsValid(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	wf := readWorkflow(t, "pr-review-autofix/workflow.yaml")
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("pr-review-autofix/workflow.yaml failed schema validation:\n%v", err)
	}
	if err := validate.Graph(&wf, nil); err != nil {
		t.Errorf("pr-review-autofix/workflow.yaml failed graph validation:\n%v", err)
	}

	parallel := map[string]bool{"reviewer-a": false, "reviewer-b": false, "security-reviewer": false}
	for _, e := range wf.Edges {
		if e.From == "fetch-diff" {
			if _, ok := parallel[e.To]; ok {
				parallel[e.To] = true
			}
		}
	}
	for id, fedByDiff := range parallel {
		if !fedByDiff {
			t.Errorf("%s should be a direct child of fetch-diff (the parallel-reviewer shape VISION.md describes)", id)
		}
	}

	// M2.13's real run proved that artifact placeholders resolve only from a
	// direct parent: each mutating node that reads fixer fields must retain its
	// own data edge, independently of the ordering/gating edges.
	fixerDataEdges := map[string]bool{"apply-fix": false, "stage": false, "commit": false, "push-plan": false, "create-blob": false, "create-tree": false, "create-commit": false}
	for _, e := range wf.Edges {
		if e.From == "fixer" {
			if _, ok := fixerDataEdges[e.To]; ok {
				fixerDataEdges[e.To] = true
			}
		}
	}
	for id, fedByFixer := range fixerDataEdges {
		if !fedByFixer {
			t.Errorf("%s references a fixer artifact and must remain a direct child of fixer", id)
		}
	}

	for _, rel := range []string{"reviewer-a.worker.yaml", "reviewer-b.worker.yaml", "security-reviewer.worker.yaml", "locate-file.worker.yaml", "fixer.worker.yaml", "verify-fix.worker.yaml", "push-plan.worker.yaml"} {
		w := readWorker(t, "pr-review-autofix/"+rel)
		if err := v.Validate(validate.KindWorker, w, nil); err != nil {
			t.Errorf("%s failed schema validation:\n%v", rel, err)
		}
		if _, err := validate.CompileSchema(w.Contract.OutputSchema); err != nil {
			t.Errorf("%s output schema does not compile: %v", rel, err)
		}
		if w.Description == "" {
			t.Errorf("%s has no human-facing description (REQ-WORKER-08)", rel)
		}
	}

	var modelNodes, deterministicNodes int
	for _, node := range wf.Nodes {
		if node.Worker != "" {
			modelNodes++
		}
		if node.Tool != nil {
			deterministicNodes++
		}
	}
	if modelNodes != 7 || deterministicNodes != 14 {
		t.Errorf("model/deterministic split = %d/%d, want 7/14 (push-plan + the GitHub push/PR tail)", modelNodes, deterministicNodes)
	}
	if facts := registry.DeriveTemplateFacts(wf); !facts.WriteCapable {
		t.Error("pr-review-autofix must remain visibly write-capable; approval checkpoints guard its mutations")
	}
}

// TestSecondaryDemosAreValid covers the three secondary demos (VISION.md,
// M1.14's template gallery): bug-investigation, prd-generation,
// architecture-review — every workflow validates against schema+graph rules,
// and every sibling Worker's Contract compiles.
func TestSecondaryDemosAreValid(t *testing.T) {
	v, err := validate.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	for _, dir := range []string{"bug-investigation", "prd-generation", "architecture-review", "test-generator", "change-risk", "refactor-plan", "release-notes", "dependency-audit"} {
		validateExampleDir(t, v, dir)
	}
}

// TestBugInvestigationHasVerifierNode locks in REQ-CONTRACT-05: verify-patch
// is a distinct Worker from patch (the producer it judges), gating a
// conditional edge — never the producer gating itself.
func TestBugInvestigationHasVerifierNode(t *testing.T) {
	wf := readWorkflow(t, "bug-investigation/workflow.yaml")
	var gate *domain.Edge
	for i, e := range wf.Edges {
		if e.To == "apply-patch" {
			gate = &wf.Edges[i]
		}
	}
	if gate == nil || gate.Condition == nil {
		t.Fatal("apply-patch should be gated by a conditional edge")
	}
	if gate.From == "patch" {
		t.Error("the gate should come from verify-patch (a separate judge), not from patch (the producer) itself")
	}
	if gate.From != "verify-patch" {
		t.Errorf("gate.From = %q, want verify-patch", gate.From)
	}
}

// validateExampleDir validates dir's workflow.yaml (schema + graph) plus
// every sibling *.worker.yaml (schema + compilable output Contract) — the
// same checks TestExamplesAreValid/TestFlagshipIsValid apply by hand, kept
// here as a helper so a new example directory is one line to cover.
func validateExampleDir(t *testing.T, v *validate.Validator, dir string) {
	t.Helper()
	wf := readWorkflow(t, filepath.Join(dir, "workflow.yaml"))
	if err := v.Validate(validate.KindWorkflow, wf, nil); err != nil {
		t.Errorf("%s/workflow.yaml failed schema validation:\n%v", dir, err)
	}
	if err := validate.Graph(&wf, nil); err != nil {
		t.Errorf("%s/workflow.yaml failed graph validation:\n%v", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".worker.yaml") {
			continue
		}
		found++
		rel := filepath.Join(dir, e.Name())
		w := readWorker(t, rel)
		if err := v.Validate(validate.KindWorker, w, nil); err != nil {
			t.Errorf("%s failed schema validation:\n%v", rel, err)
		}
		if _, err := validate.CompileSchema(w.Contract.OutputSchema); err != nil {
			t.Errorf("%s output schema does not compile: %v", rel, err)
		}
	}
	if found == 0 {
		t.Errorf("%s: no *.worker.yaml files found", dir)
	}
}

// TestExampleContractsAreTight locks in REQ-CONTRACT-04: the flagship example's
// output schema uses bounded arrays, bounded strings, and enums — the anti-slop
// shape templates inherit. A future example that drops these bounds fails here.
func TestExampleContractsAreTight(t *testing.T) {
	worker := readWorker(t, "pr-review/reviewer.worker.yaml")
	raw, err := serialize.MarshalYAML(worker.Contract.OutputSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	schema := string(raw)
	for _, marker := range []string{"maxItems", "maxLength", "enum"} {
		if !strings.Contains(schema, marker) {
			t.Errorf("tight-contract marker %q missing from the reviewer output schema (anti-slop, REQ-CONTRACT-04)", marker)
		}
	}
}

// TestSDKFlagshipUnder100Lines is the REQ-SDK-03 acceptance test: the flagship
// PR-review demo authored via the SDK fits in at most 100 lines.
func TestSDKFlagshipUnder100Lines(t *testing.T) {
	data := readFile(t, "sdk-pr-review/main.go")
	lines := strings.Count(string(data), "\n")
	if lines > 100 {
		t.Errorf("sdk-pr-review/main.go is %d lines, want ≤ 100 (REQ-SDK-03)", lines)
	}
	t.Logf("flagship SDK demo: %d lines", lines)
}

func readWorkflow(t *testing.T, rel string) domain.Workflow {
	t.Helper()
	var wf domain.Workflow
	if err := serialize.UnmarshalYAML(readFile(t, rel), &wf); err != nil {
		t.Fatalf("decode %s: %v", rel, err)
	}
	return wf
}

func readWorker(t *testing.T, rel string) domain.Worker {
	t.Helper()
	var w domain.Worker
	if err := serialize.UnmarshalYAML(readFile(t, rel), &w); err != nil {
		t.Fatalf("decode %s: %v", rel, err)
	}
	return w
}

func readFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}
