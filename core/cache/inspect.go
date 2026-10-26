package cache

import "sort"

// List returns every cache entry, sorted by key, for `wee cache ls` (CLI wiring
// in M1.9). A cache you can't inspect is a cache you can't trust (PRIN-02).
func (c *Cache) List() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoaded(); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(c.index))
	for _, e := range c.index {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Inspect returns the entry recorded for key, for `wee cache inspect <key>`.
// The recorded artifact bytes are fetched separately from the artifact store by
// the entry's ArtifactHash — the cache holds only the reference. The engine
// reconstructs a hit's events fresh (a stored event stream would carry a stale
// executionID and break the new log's hash chain), so an entry records the node
// result, not a verbatim event log.
func (c *Cache) Inspect(key string) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoaded(); err != nil {
		return Entry{}, false, err
	}
	e, ok := c.index[key]
	return e, ok, nil
}

// Clear deletes every cache entry (`wee cache clear`). Artifacts in the shared
// store are left untouched — other executions and the audit trail still
// reference them; only the cache index is emptied.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.index = make(map[string]Entry)
	return c.save()
}

// Delete removes the named cache entries, leaving the rest of the index intact
// (REQ-CTRL-03's clear-cache-for-node/workflow granularity — the control plane
// resolves a node's key from its recorded CacheHit/CacheMiss event, then calls
// this). Keys with no entry are ignored. Like Clear, shared-store artifacts are
// never touched — only the index references are dropped. Returns the number of
// entries actually removed so a caller can report "cleared N".
func (c *Cache) Delete(keys ...string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoaded(); err != nil {
		return 0, err
	}
	removed := 0
	for _, k := range keys {
		if _, ok := c.index[k]; ok {
			delete(c.index, k)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, c.save()
}
