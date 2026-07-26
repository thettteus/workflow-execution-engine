package filesystem_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/tool/filesystem"
)

func run(t *testing.T, fs *filesystem.Tool, input string) (json.RawMessage, error) {
	t.Helper()
	return fs.Execute(context.Background(), json.RawMessage(input))
}

func TestReadWriteList(t *testing.T) {
	root := t.TempDir()
	fs := filesystem.New(root)

	if _, err := run(t, fs, `{"op":"write","path":"sub/a.txt","content":"hello"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Written under the root, in a created subdir.
	if got, err := os.ReadFile(filepath.Join(root, "sub", "a.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("file not written correctly: %q err=%v", got, err)
	}

	out, err := run(t, fs, `{"op":"read","path":"sub/a.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rd struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(out, &rd)
	if rd.Content != "hello" {
		t.Errorf("read content = %q, want hello", rd.Content)
	}

	out, err = run(t, fs, `{"op":"list","path":"sub"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !containsName(out, "a.txt") {
		t.Errorf("list did not include a.txt: %s", out)
	}
}

// TestPathTraversalRejected is a core M1.5 acceptance test (REQ-TOOL-03): every
// escape attempt fails with an error, none reads outside the root.
func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	fs := filesystem.New(root)

	// A secret living outside the root that must stay unreachable.
	outside := t.TempDir()
	secret := filepath.Join(outside, "passwd")
	if err := os.WriteFile(secret, []byte("root:x:0:0"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"dot-dot traversal": `{"op":"read","path":"../../etc/passwd"}`,
		"absolute path":     `{"op":"read","path":"` + secret + `"}`,
		"dot-dot to secret": `{"op":"read","path":"../` + filepath.Base(outside) + `/passwd"}`,
	}
	for name, input := range cases {
		if _, err := run(t, fs, input); err == nil {
			t.Errorf("%s: expected rejection, got success", name)
		}
	}
}

// TestSymlinkEscapeRejected: a symlink inside the root pointing outside it cannot
// be used to read past the boundary (REQ-TOOL-03).
func TestSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// root/escape -> outside
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	fs := filesystem.New(root)
	if _, err := run(t, fs, `{"op":"read","path":"escape/secret.txt"}`); err == nil {
		t.Error("reading through an escaping symlink must be rejected")
	}
	// A write through the symlink must also be rejected (no exfiltration outward).
	if _, err := run(t, fs, `{"op":"write","path":"escape/planted.txt","content":"x"}`); err == nil {
		t.Error("writing through an escaping symlink must be rejected")
	}
}

func containsName(listOutput json.RawMessage, name string) bool {
	var out struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(listOutput, &out)
	for _, e := range out.Entries {
		if e.Name == name {
			return true
		}
	}
	return false
}
