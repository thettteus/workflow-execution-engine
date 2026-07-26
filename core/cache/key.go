// Package cache is the local node cache: a node whose inputs haven't changed
// returns its recorded artifact instead of calling the model again, so
// re-running a workflow after changing one node re-executes only that node and
// its downstream cone (REQ-CACHE-01..04). It reuses the content-addressed
// artifact store for bytes — the cache index holds only references, never copies.
package cache

import (
	"sort"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// Inputs are the complete set of facts a node's cache key is derived from —
// nothing else, nothing less (REQ-CACHE-01). Each field is content-addressed or
// canonicalized, so the key is byte-stable across runs and machines. Any change
// to any field yields a wholly new key: invalidation is total, never partial
// (no fuzzy matching in Phase 1).
//
// This mirrors the field list in REQ-CACHE-01 as a struct rather than the
// awkward positional signature sketched in EXECUTION.md — same facts, but
// "model + parameters" is carried faithfully as a domain.ModelConfig instead of
// being flattened to strings.
type Inputs struct {
	WorkerID            string               `json:"workerId"`
	WorkerVersion       string               `json:"workerVersion"`
	ContractHash        string               `json:"contractHash"`
	InputArtifactHashes []string             `json:"inputArtifactHashes"`
	WorkflowInputs      map[string]string    `json:"workflowInputs,omitempty"`
	Model               domain.ModelConfig   `json:"model"`
	ToolVersions        []string             `json:"toolVersions"`
	ContextPolicy       domain.ContextPolicy `json:"contextPolicy"`
}

// Key returns the deterministic cache key for a node: the SHA-256 of the
// canonical JSON of its Inputs. Input-artifact hashes and tool versions are
// sorted first so edge/allowlist ordering never perturbs the key — only the
// content does.
func Key(in Inputs) string {
	norm := in
	norm.InputArtifactHashes = sortedCopy(in.InputArtifactHashes)
	norm.ToolVersions = sortedCopy(in.ToolVersions)

	// canonical.Hash cannot fail for a plain struct of strings/maps; if it ever
	// did, an empty key is safe — callers treat "" as "not cacheable" (miss).
	h, err := canonical.Hash(norm)
	if err != nil {
		return ""
	}
	return h
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
