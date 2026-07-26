package cache_test

import (
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
)

// TestDeleteRemovesOnlyNamedKeys is REQ-CTRL-03's granular clear: deleting one
// node's key leaves the rest of the index intact (unlike Clear, which empties
// everything).
func TestDeleteRemovesOnlyNamedKeys(t *testing.T) {
	c := cache.New(t.TempDir())
	for _, k := range []string{"k1", "k2", "k3"} {
		if err := c.Put(cache.Entry{Key: k, ArtifactHash: "h-" + k}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	removed, err := c.Delete("k2", "missing")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (k2 present, 'missing' ignored)", removed)
	}
	list, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("index has %d entries, want 2", len(list))
	}
	if _, ok := c.Get("k2"); ok {
		t.Error("k2 should be gone")
	}
	if _, ok := c.Get("k1"); !ok {
		t.Error("k1 should remain")
	}
}
