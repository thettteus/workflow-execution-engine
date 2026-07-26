// Package filesystem is a Tool that reads, writes, and lists files, confined to
// a single workspace root (REQ-TOOL-03, PRIN-10). Every path is resolved and
// checked before use: absolute paths, "..", and symlinks that would escape the
// root are all rejected with a distinct error — never silently followed.
package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/tool"
)

// Tool reads/writes/lists within root. root is treated as the sandbox boundary.
type Tool struct {
	root string
}

// New returns a filesystem tool confined to root.
func New(root string) *Tool { return &Tool{root: root} }

// compile-time check that Tool satisfies the interface.
var _ tool.Tool = (*Tool)(nil)
var _ tool.MutationDescriber = (*Tool)(nil)

func (t *Tool) Name() string    { return "filesystem" }
func (t *Tool) Version() string { return "1.0.0" }

func (t *Tool) InputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["op", "path"],
  "properties": {
    "op": { "enum": ["read", "write", "list"] },
    "path": { "type": "string" },
    "content": { "type": "string" }
  }
}`)
}

func (t *Tool) OutputSchema() []byte {
	return []byte(`{
  "type": "object",
  "additionalProperties": true,
  "properties": {
    "content": { "type": "string" },
    "bytesWritten": { "type": "integer" },
    "entries": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name", "isDir", "size"],
        "properties": {
          "name": { "type": "string" },
          "isDir": { "type": "boolean" },
          "size": { "type": "integer" }
        }
      }
    }
  }
}`)
}

type request struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// Execute dispatches on op. Every op resolves its path through safePath first,
// so a traversal or escape fails before any I/O.
func (t *Tool) Execute(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("filesystem: decode input: %w", err)
	}
	abs, err := t.safePath(req.Path)
	if err != nil {
		return nil, err
	}

	switch req.Op {
	case "read":
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("filesystem: read %q: %w", req.Path, err)
		}
		return marshal(map[string]any{"content": string(data)})
	case "write":
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("filesystem: create parent of %q: %w", req.Path, err)
		}
		if err := os.WriteFile(abs, []byte(req.Content), 0o644); err != nil {
			return nil, fmt.Errorf("filesystem: write %q: %w", req.Path, err)
		}
		return marshal(map[string]any{"bytesWritten": len(req.Content)})
	case "list":
		des, err := os.ReadDir(abs)
		if err != nil {
			return nil, fmt.Errorf("filesystem: list %q: %w", req.Path, err)
		}
		entries := make([]entry, 0, len(des))
		for _, de := range des {
			info, err := de.Info()
			var size int64
			if err == nil {
				size = info.Size()
			}
			entries = append(entries, entry{Name: de.Name(), IsDir: de.IsDir(), Size: size})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		return marshal(map[string]any{"entries": entries})
	default:
		return nil, fmt.Errorf("filesystem: unknown op %q", req.Op)
	}
}

// DescribeMutation classifies writes as mutating; reads/lists are non-mutating.
func (t *Tool) DescribeMutation(input json.RawMessage) (tool.Mutation, error) {
	var req request
	if err := json.Unmarshal(input, &req); err != nil {
		return tool.Mutation{}, fmt.Errorf("filesystem: decode mutation input: %w", err)
	}
	if req.Op != "write" {
		return tool.Mutation{Mutating: false, Operation: req.Op, Paths: []string{req.Path}}, nil
	}
	return tool.Mutation{
		Mutating:  true,
		Operation: "filesystem.write",
		Summary:   fmt.Sprintf("write %d bytes to %s", len(req.Content), req.Path),
		Paths:     []string{req.Path},
	}, nil
}

// safePath resolves rel against the root and rejects anything that would escape
// it. It rejects absolute inputs outright (the tool's contract is
// workspace-relative paths), neutralizes "." / ".." via Clean, and follows
// symlinks on the longest existing prefix so a symlink pointing outside the root
// cannot be used as an escape hatch.
func (t *Tool) safePath(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("filesystem: path %q must be relative to the workspace root", rel)
	}

	rootReal := t.realRoot()
	target := filepath.Join(rootReal, rel) // Join cleans embedded "." and ".."

	// Resolve symlinks on the longest existing ancestor of target, then re-append
	// the not-yet-existing suffix, so a symlinked ancestor can't smuggle us out.
	real := resolveExisting(target)

	relToRoot, err := filepath.Rel(rootReal, real)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("filesystem: path %q escapes the workspace root", rel)
	}
	return real, nil
}

// realRoot returns the symlink-resolved absolute root (falling back to a plain
// clean if the root itself can't be resolved, e.g. before it exists).
func (t *Tool) realRoot() string {
	if r, err := filepath.EvalSymlinks(t.root); err == nil {
		return r
	}
	abs, err := filepath.Abs(t.root)
	if err != nil {
		return filepath.Clean(t.root)
	}
	return abs
}

// resolveExisting returns p with symlinks resolved on its longest existing
// prefix; the remaining (not-yet-created) suffix is appended verbatim.
func resolveExisting(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(p))
}

func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("filesystem: encode output: %w", err)
	}
	return b, nil
}
