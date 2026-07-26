// Package eventlog is the append-only, per-execution event log plus the frozen
// execution snapshot. Together they make an execution directory
// (<baseDir>/executions/<id>/) self-sufficient: its full timeline can be
// reconstructed from disk alone, with no in-process state — the foundation
// audit replay (M1.7) is built on.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
)

const (
	eventsFile   = "events.jsonl"
	snapshotFile = "snapshot.json"
)

// genesisHash is the predecessor hash used for the first event of an execution
// that has no snapshot on disk. When a snapshot exists (every engine-driven
// run), genesis chains from the snapshot's canonical hash instead.
const genesisHash = ""

// Log reads and writes execution records under <baseDir>/executions.
type Log struct {
	baseDir string
	mu      sync.Mutex        // serializes appends so concurrent lines never interleave
	lastCh  map[string]string // per-execution hash of the last-appended event (hash-chain head)
}

// New returns a Log rooted at <baseDir>/executions. baseDir is the workspace
// root (conventionally ".workflow").
func New(baseDir string) *Log {
	return &Log{baseDir: baseDir, lastCh: make(map[string]string)}
}

// lineHash is the SHA-256 of an event's exact on-disk line bytes — the value a
// successor stores in its PrevHash. Hashing the raw line (not the re-marshaled
// struct) makes the chain sensitive to *any* byte change, including fields the
// Event struct would otherwise ignore on decode. Computed one way, everywhere
// (ADR 0003). The trailing newline is never part of the hashed bytes.
func lineHash(raw []byte) string {
	return canonical.HashBytes(raw)
}

// snapshotHash returns the canonical hash of an execution's snapshot.json, or
// genesisHash if no snapshot exists. The snapshot is already canonical bytes on
// disk, so we hash them directly.
func (l *Log) snapshotHash(executionID string) (string, error) {
	data, err := os.ReadFile(filepath.Join(l.baseDir, "executions", executionID, snapshotFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return genesisHash, nil
		}
		return "", fmt.Errorf("eventlog: read snapshot for chain genesis: %w", err)
	}
	return canonical.HashBytes(data), nil
}

// headHash returns the hash the next appended event must chain from: the cached
// head if known, else recomputed from the log's last line on disk, else the
// snapshot's hash (genesis) for an empty/absent log. Callers hold l.mu.
func (l *Log) headHash(executionID string) (string, error) {
	if h, ok := l.lastCh[executionID]; ok {
		return h, nil
	}
	path := filepath.Join(l.baseDir, "executions", executionID, eventsFile)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return l.snapshotHash(executionID)
		}
		return "", fmt.Errorf("eventlog: open log for chain head: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	var lastHash string
	seen := false
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		lastHash, seen = lineHash(raw), true
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("eventlog: scan log for chain head: %w", err)
	}
	if !seen {
		return l.snapshotHash(executionID)
	}
	return lastHash, nil
}

// dir returns the directory for an execution, creating it if needed.
func (l *Log) dir(executionID string) (string, error) {
	d := filepath.Join(l.baseDir, "executions", executionID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("eventlog: create %s: %w", d, err)
	}
	return d, nil
}

// Append writes ev as one JSON line to the execution's events.jsonl, chaining it
// to its predecessor: ev.PrevHash is set to the hash of the last-written event
// (or the snapshot's hash for the first event), making the log tamper-evident
// (ADR 0007). Appends are serialized, so lines are never interleaved and the
// chain stays strictly ordered under concurrent writers. PrevHash on the passed
// event is ignored and overwritten.
func (l *Log) Append(executionID string, ev domain.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	prev, err := l.headHash(executionID)
	if err != nil {
		return err
	}
	ev.PrevHash = prev

	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventlog: marshal event: %w", err)
	}

	d, err := l.dir(executionID)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(d, eventsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("eventlog: open log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("eventlog: append: %w", err)
	}
	l.lastCh[executionID] = lineHash(line)
	return nil
}
