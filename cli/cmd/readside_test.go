package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runToolAndID runs the tool workflow in a fresh workspace and returns the
// execution id the run recorded (read from the executions directory), so the
// read-side commands have a real execution to operate on.
func runToolAndID(t *testing.T) string {
	t.Helper()
	wf := setupToolRun(t, toolWorkflow)
	if _, err := execCLI(t, "run", wf, "--allow-mutations-without-approval"); err != nil {
		t.Fatalf("run: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspaceDir, "executions"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no executions recorded: %v", err)
	}
	return entries[0].Name()
}

func TestReplayAuditReadsRecordedRun(t *testing.T) {
	id := runToolAndID(t)
	out, err := execCLI(t, "replay", id)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !strings.Contains(out, id) {
		t.Errorf("replay output missing the execution id:\n%s", out)
	}
	if !strings.Contains(out, "echo") || !strings.Contains(out, "succeeded") {
		t.Errorf("expected the echo node to show succeeded:\n%s", out)
	}
	if !strings.Contains(out, "integrity: hash chain verified") {
		t.Errorf("replay audit should report the verified hash chain:\n%s", out)
	}
}

// TestReplayAuditFailsOnTamperedLog: the audit verifies the hash chain, so an
// edited event line is reported and fails the command (exit 3) instead of
// rendering a clean-looking timeline.
func TestReplayAuditFailsOnTamperedLog(t *testing.T) {
	id := runToolAndID(t)
	path := filepath.Join(workspaceDir, "executions", id, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	tampered := strings.Replace(string(data), `"WorkerStarted"`, `"WorkerStarted","note":"edited"`, 1)
	if tampered == string(data) {
		t.Fatal("fixture has no WorkerStarted event to tamper with")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	out, err := execCLI(t, "replay", id)
	if err == nil {
		t.Fatalf("replay of a tampered log should fail:\n%s", out)
	}
	if !strings.Contains(out, "integrity: FAILED") {
		t.Errorf("replay output should name the integrity failure:\n%s", out)
	}
}

func TestInspectNodeShowsArtifact(t *testing.T) {
	id := runToolAndID(t)
	out, err := execCLI(t, "inspect", id, "--node", "echo")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !strings.Contains(out, "artifact:") || !strings.Contains(out, "hello from wee") {
		t.Errorf("inspect --node did not show the artifact content:\n%s", out)
	}
}

func TestInspectUnknownNodeErrors(t *testing.T) {
	id := runToolAndID(t)
	if _, err := execCLI(t, "inspect", id, "--node", "nope"); err == nil {
		t.Error("inspecting an unknown node should error")
	}
}

func TestListShowsWorkflowAndExecution(t *testing.T) {
	id := runToolAndID(t)
	out, err := execCLI(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "toolflow") {
		t.Errorf("list did not show the workflow:\n%s", out)
	}
	if !strings.Contains(out, id) {
		t.Errorf("list did not show the execution %s:\n%s", id, out)
	}
}

func TestCacheClear(t *testing.T) {
	runToolAndID(t)
	out, err := execCLI(t, "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear: %v", err)
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("expected a 'cleared' confirmation, got: %q", out)
	}
}

func TestExportRoundTrips(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte(validWorkflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	// validWorkflow references greeter@1.0.0; provide the worker so export finds it.
	worker := "id: greeter\nversion: 1.0.0\nobjective: greet\nconstraints: []\ntools: []\ncontextPolicy:\n  mode: none\ncontract:\n  goal: g\n  rules: []\n  successCriteria: []\n  maxRetries: 0\n  outputSchema:\n    type: object\nmodel:\n  provider: openai\n  model: gpt-4o-mini\n"
	if err := os.WriteFile(filepath.Join(dir, "greeter.worker.yaml"), []byte(worker), 0o644); err != nil {
		t.Fatalf("write worker: %v", err)
	}

	out, err := execCLI(t, "export", "wf.yaml")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out, "hello-1.0.0.tar") {
		t.Errorf("expected the bundle path in output, got: %q", out)
	}
	// M2.3: export also declares read-only/write-capable + tools — a
	// worker-only node (no Tool) must read as read-only with no tools.
	if !strings.Contains(out, "read-only") || !strings.Contains(out, "tools: none") {
		t.Errorf("expected a read-only/tools:none declaration line, got: %q", out)
	}
	info, err := os.Stat("hello-1.0.0.tar")
	if err != nil || info.Size() == 0 {
		t.Errorf("export did not produce a non-empty bundle: %v", err)
	}
}
