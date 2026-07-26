package serialize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// MarshalJSON encodes v as indented JSON.
func MarshalJSON(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

// UnmarshalJSON decodes JSON into v (a pointer), rejecting unknown fields so a
// stray or misspelled key surfaces as an error rather than being dropped.
func UnmarshalJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// LoadWorkflow reads a Workflow from a .yaml/.yml or .json file, chosen by the
// file extension. It is the headline load API; the format helpers above cover
// the other domain types.
func LoadWorkflow(path string) (*domain.Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wf domain.Workflow
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		err = UnmarshalYAML(data, &wf)
	case ".json":
		err = UnmarshalJSON(data, &wf)
	default:
		return nil, fmt.Errorf("serialize: unsupported workflow extension %q (want .yaml, .yml, or .json)", ext)
	}
	if err != nil {
		return nil, fmt.Errorf("serialize: loading %s: %w", path, err)
	}
	return &wf, nil
}

// SaveWorkflow writes a Workflow to path, choosing the format by extension.
func SaveWorkflow(wf *domain.Workflow, path string) error {
	var (
		data []byte
		err  error
	)
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		data, err = MarshalYAML(wf)
	case ".json":
		data, err = MarshalJSON(wf)
	default:
		return fmt.Errorf("serialize: unsupported workflow extension %q (want .yaml, .yml, or .json)", ext)
	}
	if err != nil {
		return fmt.Errorf("serialize: encoding %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}
