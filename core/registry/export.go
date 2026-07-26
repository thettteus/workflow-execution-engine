package registry

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
)

// archive layout: one workflow.json plus one workers/<id@version>.json per
// worker the workflow references. Entry bytes are canonical (ADR 0004), so the
// content hash of each definition survives an export→import round-trip
// unchanged (REQ-DEF-02) — the property REQ-VERSION-03 requires.
const (
	workflowEntryName = "workflow.json"
	workerDirPrefix   = "workers/"
)

// Export bundles the workflow name@version and every worker it references into
// a single portable tar archive (REQ-VERSION-03). Each entry is canonical JSON,
// so importing the archive elsewhere yields byte-identical content hashes
// (REQ-DEF-02).
//
// Secrets never travel in the bundle: a definition holds only secret
// *references* (`${env:NAME}` — the name, never the value; NFR-SEC-01), and
// Export serializes definitions verbatim without resolving anything, so no
// resolved secret value can appear. The references themselves are preserved on
// purpose — they are what tells the importer which environment variables to
// supply, and stripping them would break portability.
func (r *Registry) Export(name, version string) ([]byte, error) {
	ref := name + "@" + version
	wfe, ok := r.workflows[ref]
	if !ok {
		return nil, fmt.Errorf("registry: no workflow %q registered to export", ref)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := writeCanonicalEntry(tw, workflowEntryName, wfe.def); err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, n := range wfe.def.Nodes {
		if n.Worker == "" || seen[n.Worker] {
			continue
		}
		we, ok := r.workers[n.Worker]
		if !ok {
			return nil, fmt.Errorf("registry: workflow %q references unregistered worker %q — cannot export a partial bundle", ref, n.Worker)
		}
		seen[n.Worker] = true
		if err := writeCanonicalEntry(tw, workerDirPrefix+n.Worker+".json", we.def); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("registry: finalize export archive: %w", err)
	}
	return buf.Bytes(), nil
}

// writeCanonicalEntry marshals v canonically and writes it as one tar entry.
func writeCanonicalEntry(tw *tar.Writer, name string, v any) error {
	data, err := canonical.Marshal(v)
	if err != nil {
		return fmt.Errorf("registry: marshal %s: %w", name, err)
	}
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("registry: write header for %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("registry: write %s: %w", name, err)
	}
	return nil
}

// Import reads a bundle produced by Export into a fresh Registry, re-registering
// the workflow and its workers. Because Register* recompute each definition's
// canonical hash, the imported hashes equal the exporter's (REQ-DEF-02), and
// the immutability guarantee (REQ-VERSION-01) applies on the importing side too.
func Import(archive []byte) (*Registry, error) {
	r := New()
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("registry: read import archive: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("registry: read entry %q: %w", hdr.Name, err)
		}
		switch {
		case hdr.Name == workflowEntryName:
			var wf domain.Workflow
			if err := serialize.UnmarshalJSON(data, &wf); err != nil {
				return nil, fmt.Errorf("registry: decode %s: %w", hdr.Name, err)
			}
			if err := r.RegisterWorkflow(wf); err != nil {
				return nil, err
			}
		case strings.HasPrefix(hdr.Name, workerDirPrefix):
			var w domain.Worker
			if err := serialize.UnmarshalJSON(data, &w); err != nil {
				return nil, fmt.Errorf("registry: decode %s: %w", hdr.Name, err)
			}
			if err := r.RegisterWorker(w); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("registry: unexpected entry %q in import archive", hdr.Name)
		}
	}
	return r, nil
}
