package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
)

// WriteSnapshot freezes the fully-resolved graph + config for an execution to
// snapshot.json. It is written once, at ExecutionStarted, and never mutated;
// audit replay (M1.7) reads it instead of re-resolving anything live. The bytes
// are canonical (sorted keys) so the snapshot is deterministic and hashable.
func (l *Log) WriteSnapshot(executionID string, v any) error {
	data, err := canonical.Marshal(v)
	if err != nil {
		return fmt.Errorf("eventlog: marshal snapshot: %w", err)
	}
	d, err := l.dir(executionID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, snapshotFile), data, 0o644); err != nil {
		return fmt.Errorf("eventlog: write snapshot: %w", err)
	}
	return nil
}

// ReadSnapshot decodes an execution's snapshot.json into dst (a pointer).
func (l *Log) ReadSnapshot(executionID string, dst any) error {
	path := filepath.Join(l.baseDir, "executions", executionID, snapshotFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("eventlog: read snapshot for %s: %w", executionID, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("eventlog: decode snapshot for %s: %w", executionID, err)
	}
	return nil
}
