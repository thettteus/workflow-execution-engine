package git_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gittool "github.com/thettteus/workflow-execution-engine/core/tool/git"
)

// initRepo makes a temp git repo with identity configured so commits work in CI.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

func exec1(t *testing.T, tool *gittool.Tool, input string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("git Execute(%s): %v", input, err)
	}
	var r struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal(out, &r)
	return r.Output
}

func TestStatusAddCommitDiffBranch(t *testing.T) {
	dir := initRepo(t)
	g := gittool.New(dir, 0)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// status shows the untracked file.
	if out := exec1(t, g, `{"op":"status"}`); !strings.Contains(out, "a.txt") {
		t.Errorf("status should list a.txt, got %q", out)
	}
	// add + commit.
	exec1(t, g, `{"op":"add","paths":["a.txt"]}`)
	exec1(t, g, `{"op":"commit","message":"add a"}`)
	// After commit, a clean tree has empty porcelain status.
	if out := exec1(t, g, `{"op":"status"}`); strings.TrimSpace(out) != "" {
		t.Errorf("status should be clean after commit, got %q", out)
	}
	// branch (list) mentions a branch; create a new branch.
	exec1(t, g, `{"op":"branch","name":"feature"}`)
	if out := exec1(t, g, `{"op":"branch"}`); !strings.Contains(out, "feature") {
		t.Errorf("branch list should include the new branch, got %q", out)
	}
	// diff after a change.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := exec1(t, g, `{"op":"diff"}`); !strings.Contains(out, "two") {
		t.Errorf("diff should show the change, got %q", out)
	}
}

// TestNoPush confirms push is not a reachable op — the workflow engine never
// touches a remote in Phase 1.
func TestNoPush(t *testing.T) {
	g := gittool.New(t.TempDir(), 0)
	if _, err := g.Execute(context.Background(), json.RawMessage(`{"op":"push"}`)); err == nil {
		t.Fatal("push must not be a supported git op")
	}
}
