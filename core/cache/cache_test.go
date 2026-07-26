package cache_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
)

func baseInputs() cache.Inputs {
	return cache.Inputs{
		WorkerID:            "reviewer",
		WorkerVersion:       "1.0.0",
		ContractHash:        "abc",
		InputArtifactHashes: []string{"h1", "h2"},
		Model:               domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini", Params: map[string]any{"temperature": 0}},
		ToolVersions:        []string{"git", "filesystem"},
		ContextPolicy:       domain.ContextPolicy{Mode: domain.ContextDiffOnly},
	}
}

// TestKeyDeterministicAndOrderInsensitive: the same facts yield the same key,
// and input-hash / tool ordering does not perturb it (REQ-CACHE-01).
func TestKeyDeterministicAndOrderInsensitive(t *testing.T) {
	a := cache.Key(baseInputs())
	if a == "" {
		t.Fatal("key should not be empty")
	}
	if a != cache.Key(baseInputs()) {
		t.Error("same inputs must yield the same key")
	}

	reordered := baseInputs()
	reordered.InputArtifactHashes = []string{"h2", "h1"}
	reordered.ToolVersions = []string{"filesystem", "git"}
	if cache.Key(reordered) != a {
		t.Error("input-hash/tool ordering must not change the key")
	}
}

// TestKeyChangesOnAnyFieldChange: total invalidation, no partial matching.
func TestKeyChangesOnAnyFieldChange(t *testing.T) {
	base := cache.Key(baseInputs())
	mutate := map[string]func(*cache.Inputs){
		"worker version": func(i *cache.Inputs) { i.WorkerVersion = "1.0.1" },
		"contract hash":  func(i *cache.Inputs) { i.ContractHash = "def" },
		"input hash":     func(i *cache.Inputs) { i.InputArtifactHashes = []string{"h1", "h3"} },
		"workflow input": func(i *cache.Inputs) { i.WorkflowInputs = map[string]string{"prUrl": "https://example.com/other"} },
		"model":          func(i *cache.Inputs) { i.Model.Model = "gpt-4o" },
		"model params":   func(i *cache.Inputs) { i.Model.Params = map[string]any{"temperature": 1} },
		"tools":          func(i *cache.Inputs) { i.ToolVersions = []string{"git"} },
		"context policy": func(i *cache.Inputs) { i.ContextPolicy = domain.ContextPolicy{Mode: domain.ContextParentOnly} },
	}
	for name, m := range mutate {
		in := baseInputs()
		m(&in)
		if cache.Key(in) == base {
			t.Errorf("changing %s must change the key", name)
		}
	}
}

func TestStoreRoundTripAndPersistence(t *testing.T) {
	base := t.TempDir()
	c := cache.New(base)
	e := cache.Entry{Key: "k1", ArtifactHash: "hhh", ArtifactType: domain.ArtifactJSON, CostUSD: 0.02, Tokens: 42}
	if err := c.Put(e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A fresh Cache over the same dir reads the persisted entry from disk.
	got, ok := cache.New(base).Get("k1")
	if !ok {
		t.Fatal("entry not persisted across Cache instances")
	}
	if got.ArtifactHash != "hhh" || got.CostUSD != 0.02 || got.Tokens != 42 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if _, ok := c.Get("missing"); ok {
		t.Error("Get returned an entry that was never put")
	}
}

func TestListAndClear(t *testing.T) {
	base := t.TempDir()
	c := cache.New(base)
	_ = c.Put(cache.Entry{Key: "b"})
	_ = c.Put(cache.Entry{Key: "a"})
	list, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Key != "a" || list[1].Key != "b" {
		t.Errorf("List should be sorted by key: %+v", list)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if list, _ := c.List(); len(list) != 0 {
		t.Errorf("Clear should empty the index, got %d entries", len(list))
	}
}
