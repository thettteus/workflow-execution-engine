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

// ChainError reports the first break in an execution's event hash chain: the
// line whose recorded PrevHash does not match the canonical hash of the event
// before it (or, for the first line, the snapshot's hash). Line is 1-based.
type ChainError struct {
	ExecutionID string
	Line        int // 1-based line number of the offending event
	Want        string
	Got         string
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("eventlog: chain broken in %s at line %d: event records prevHash %q but predecessor hashes to %q",
		e.ExecutionID, e.Line, e.Got, e.Want)
}

// Verify walks an execution's event log and confirms every event chains to its
// predecessor (REQ-EVENT-03, ADR 0007). It returns a *ChainError naming the
// first break — an edit, deletion, or reorder of any recorded event — or nil if
// the chain is intact. A missing log is an error.
//
// Each event's PrevHash is checked against the SHA-256 of the *raw bytes* of the
// line before it (the first against the snapshot's hash), so any byte-level
// tamper is caught, including fields the Event struct would ignore on decode.
//
// The chain proves internal consistency: any change to a middle event breaks the
// link its successor recorded. It does not detect tail truncation on its own —
// a wholesale rewrite from a break point onward re-chains cleanly — which is why
// ADR 0007 scopes external anchoring to a possible later hardening.
func (l *Log) Verify(executionID string) error {
	path := filepath.Join(l.baseDir, "executions", executionID, eventsFile)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("eventlog: open log for %s: %w", executionID, err)
	}
	defer f.Close()

	prev, err := l.snapshotHash(executionID)
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	line := 0
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		line++
		var ev domain.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return fmt.Errorf("eventlog: verify %s line %d: %w", path, line, err)
		}
		if ev.PrevHash != prev {
			return &ChainError{ExecutionID: executionID, Line: line, Want: prev, Got: ev.PrevHash}
		}
		prev = lineHash(raw)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("eventlog: scanning %s: %w", path, err)
	}
	return nil
}
