package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

const indexFile = "index.json"

// Entry is what the cache records for one key: a reference to the node's
// artifact (held in the shared content-addressed store, never copied here) plus
// the original cost/tokens, kept so a hit can report what it saved
// (REQ-METRIC-03) without re-deriving it.
type Entry struct {
	Key          string              `json:"key"`
	ArtifactHash string              `json:"artifactHash"`
	ArtifactType domain.ArtifactType `json:"artifactType"`
	CostUSD      float64             `json:"costUsd"`
	Tokens       int64               `json:"tokens"`
	// CreatedAt is an RFC3339 string (not time.Time) so the JSON index stays
	// trivially diffable and free of monotonic-clock noise. Set by the caller.
	CreatedAt string `json:"createdAt,omitempty"`
}

// Cache is a file-backed key→Entry index under <baseDir>/cache/index.json. The
// artifact bytes themselves live in <baseDir>/artifacts (core/store); the index
// holds only references, so nothing is stored twice. Safe for concurrent use.
type Cache struct {
	dir    string
	mu     sync.Mutex
	loaded bool
	index  map[string]Entry
}

// New returns a cache rooted at <baseDir>/cache. The index is loaded lazily on
// first access. baseDir is the workspace root — the same one core/store and
// core/eventlog use — so cache hits reference artifacts the store already holds.
func New(baseDir string) *Cache {
	return &Cache{dir: filepath.Join(baseDir, "cache"), index: make(map[string]Entry)}
}

// Get returns the entry for key, if present.
func (c *Cache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoaded(); err != nil {
		return Entry{}, false
	}
	e, ok := c.index[key]
	return e, ok
}

// Put records (or replaces) an entry and persists the index.
func (c *Cache) Put(e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoaded(); err != nil {
		return err
	}
	c.index[e.Key] = e
	return c.save()
}

// ensureLoaded reads the index from disk once. A missing index is an empty
// cache, not an error. Callers hold c.mu.
func (c *Cache) ensureLoaded() error {
	if c.loaded {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(c.dir, indexFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.loaded = true
			return nil
		}
		return fmt.Errorf("cache: read index: %w", err)
	}
	if err := json.Unmarshal(data, &c.index); err != nil {
		return fmt.Errorf("cache: decode index: %w", err)
	}
	c.loaded = true
	return nil
}

// save writes the index atomically (temp file + rename) so a concurrent reader
// never sees a half-written index. Callers hold c.mu.
func (c *Cache) save() error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("cache: create dir: %w", err)
	}
	data, err := json.MarshalIndent(c.index, "", "  ")
	if err != nil {
		return fmt.Errorf("cache: encode index: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, "index-*.tmp")
	if err != nil {
		return fmt.Errorf("cache: temp index: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("cache: write index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: close index: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(c.dir, indexFile)); err != nil {
		return fmt.Errorf("cache: commit index: %w", err)
	}
	return nil
}
