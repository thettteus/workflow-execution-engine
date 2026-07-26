package replay

import (
	"archive/tar"
	"bytes"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// bundle tar entry names. The layout mirrors an execution's on-disk directory
// plus the artifacts it references, so the archive alone is enough to audit-
// replay the run elsewhere (M1.2's "an execution directory alone reconstructs
// the timeline", now portable).
const (
	bundleSnapshotName = "snapshot.json"
	bundleEventsName   = "events.jsonl"
	bundleArtifactDir  = "artifacts/"
)

// ExportBundle packages one recorded execution — its frozen snapshot, its full
// event log, and every artifact those events reference — into a single portable
// tar archive (REQ-CTRL-03's export-execution-bundle, ADR 0012).
//
// The events.jsonl bytes are copied verbatim, never re-marshaled, so the hash
// chain (ADR 0007) verifies unchanged in the copy. Secrets never travel: events
// are already redacted at write time (NFR-SEC-01) and the snapshot holds only
// secret *references* (`${env:NAME}`), so — like registry.Export — no resolved
// secret value can appear.
func ExportBundle(log *eventlog.Log, st *store.Store, executionID string) ([]byte, error) {
	snap, err := log.RawSnapshot(executionID)
	if err != nil {
		return nil, fmt.Errorf("replay: bundle %s: read snapshot: %w", executionID, err)
	}
	events, err := log.RawEvents(executionID)
	if err != nil {
		return nil, fmt.Errorf("replay: bundle %s: read events: %w", executionID, err)
	}
	// The decoded events tell us which artifacts to include (verbatim copy above
	// stays the source of truth for the bytes; this read is only for the hashes).
	decoded, err := log.ReadAll(executionID)
	if err != nil {
		return nil, fmt.Errorf("replay: bundle %s: %w", executionID, err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeBundleEntry(tw, bundleSnapshotName, snap); err != nil {
		return nil, err
	}
	if err := writeBundleEntry(tw, bundleEventsName, events); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, ev := range decoded {
		if ev.Type != domain.ArtifactCreated {
			continue
		}
		hash, _ := ev.Payload["hash"].(string)
		if hash == "" || seen[hash] {
			continue
		}
		seen[hash] = true
		content, err := st.Get(hash)
		if err != nil {
			return nil, fmt.Errorf("replay: bundle %s: reload artifact %s: %w", executionID, hash, err)
		}
		if err := writeBundleEntry(tw, bundleArtifactDir+hash, content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("replay: bundle %s: finalize: %w", executionID, err)
	}
	return buf.Bytes(), nil
}

func writeBundleEntry(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
		return fmt.Errorf("replay: bundle header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("replay: bundle write %s: %w", name, err)
	}
	return nil
}
