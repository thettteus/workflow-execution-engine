package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// maxLine is the largest single event line the reader will accept. Payloads can
// be large; the default bufio.Scanner limit (64 KiB) is too small.
const maxLine = 16 * 1024 * 1024

// Dir returns an execution's record directory (<baseDir>/executions/<id>)
// without creating it — for read-only consumers like bundle export (M2.2). The
// files under it may or may not exist.
func (l *Log) Dir(executionID string) string {
	return filepath.Join(l.baseDir, "executions", executionID)
}

// RawSnapshot returns the exact bytes of an execution's snapshot.json.
func (l *Log) RawSnapshot(executionID string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.Dir(executionID), snapshotFile))
}

// RawEvents returns the exact bytes of an execution's events.jsonl — verbatim,
// not re-marshaled, so the hash chain (ADR 0007) verifies unchanged in a copy
// (bundle export, M2.2).
func (l *Log) RawEvents(executionID string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.Dir(executionID), eventsFile))
}

// ReadAll streams an execution's events.jsonl back into a slice, in write order.
// A missing log is reported as an error; an empty log yields an empty slice.
func (l *Log) ReadAll(executionID string) ([]domain.Event, error) {
	path := filepath.Join(l.baseDir, "executions", executionID, eventsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open log for %s: %w", executionID, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	var events []domain.Event
	for line := 0; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var ev domain.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("eventlog: %s line %d: %w", path, line+1, err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: scanning %s: %w", path, err)
	}
	return events, nil
}
