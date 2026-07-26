package replay_test

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// TestExportBundleContainsSnapshotEventsArtifacts is REQ-CTRL-03's
// export-execution-bundle: the archive carries the frozen snapshot, the full
// event log copied verbatim (hash chain intact), and every referenced artifact.
func TestExportBundleContainsSnapshotEventsArtifacts(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)

	content := []byte("generated source")
	hash, err := st.Put(content)
	if err != nil {
		t.Fatalf("store put: %v", err)
	}
	if err := log.WriteSnapshot("e1", engine.Snapshot{Workflow: domain.Workflow{ID: "wf", Version: "1.0.0", Nodes: []domain.Node{{ID: "a"}}}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	for _, ev := range []domain.Event{
		{Type: domain.ExecutionStarted, ExecutionID: "e1", Payload: map[string]any{"workflow": "wf", "version": "1.0.0"}},
		{Type: domain.ArtifactCreated, ExecutionID: "e1", NodeID: "a", Payload: map[string]any{"hash": hash, "type": "code"}},
		{Type: domain.WorkerFinished, ExecutionID: "e1", NodeID: "a", Payload: map[string]any{}},
		{Type: domain.ExecutionFinished, ExecutionID: "e1", Payload: map[string]any{"state": "succeeded"}},
	} {
		if err := log.Append("e1", ev); err != nil {
			t.Fatalf("append %s: %v", ev.Type, err)
		}
	}

	archive, err := replay.ExportBundle(log, st, "e1")
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}

	entries := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		b, _ := io.ReadAll(tr)
		entries[h.Name] = b
	}

	if _, ok := entries["snapshot.json"]; !ok {
		t.Error("bundle missing snapshot.json")
	}
	rawEvents, _ := log.RawEvents("e1")
	if !bytes.Equal(entries["events.jsonl"], rawEvents) {
		t.Error("bundle events.jsonl is not a verbatim copy (hash chain would break)")
	}
	if got := entries["artifacts/"+hash]; !bytes.Equal(got, content) {
		t.Errorf("bundle artifact mismatch: got %q, want %q", got, content)
	}
}
