package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/thettteus/workflow-execution-engine/schemas"
)

// Kind identifies which domain schema to validate against.
type Kind string

const (
	KindWorkflow      Kind = "workflow"
	KindWorker        Kind = "worker"
	KindContract      Kind = "contract"
	KindContextPolicy Kind = "context-policy"
	KindArtifact      Kind = "artifact"
	KindEvent         Kind = "event"
	KindExecution     Kind = "execution"
	KindBudget        Kind = "budget"
)

// schemaID maps a Kind to the $id of its schema resource.
var schemaID = map[Kind]string{
	KindWorkflow:      "https://wee.dev/schemas/workflow.schema.json",
	KindWorker:        "https://wee.dev/schemas/worker.schema.json",
	KindContract:      "https://wee.dev/schemas/contract.schema.json",
	KindContextPolicy: "https://wee.dev/schemas/context-policy.schema.json",
	KindArtifact:      "https://wee.dev/schemas/artifact.schema.json",
	KindEvent:         "https://wee.dev/schemas/event.schema.json",
	KindExecution:     "https://wee.dev/schemas/execution.schema.json",
	KindBudget:        "https://wee.dev/schemas/budget.schema.json",
}

// Validator compiles the embedded JSON Schemas once and validates domain
// objects against them. It is safe for concurrent use after construction.
type Validator struct {
	compiled map[Kind]*jsonschema.Schema
	printer  *message.Printer
}

// NewValidator compiles every embedded schema (resolving cross-file $refs).
func NewValidator() (*Validator, error) {
	c := jsonschema.NewCompiler()

	entries, err := fs.ReadDir(schemas.FS, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		data, err := schemas.FS.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("validate: parsing %s: %w", e.Name(), err)
		}
		id, ok := docID(doc)
		if !ok {
			return nil, fmt.Errorf("validate: %s has no $id", e.Name())
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("validate: adding %s: %w", e.Name(), err)
		}
	}

	v := &Validator{
		compiled: make(map[Kind]*jsonschema.Schema, len(schemaID)),
		printer:  message.NewPrinter(language.English),
	}
	for kind, id := range schemaID {
		sch, err := c.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("validate: compiling %s schema: %w", kind, err)
		}
		v.compiled[kind] = sch
	}
	return v, nil
}

// SchemaError is a schema-validation failure carrying one Problem per leaf
// violation.
type SchemaError struct {
	Kind     Kind
	src      LineResolver
	Problems []Problem
}

func (e *SchemaError) Error() string {
	return formatProblems(fmt.Sprintf("%s failed schema validation", e.Kind), e.src, e.Problems)
}

// Validate checks obj against the schema for kind. src is optional; when
// provided, problems are annotated with source line numbers.
func (v *Validator) Validate(kind Kind, obj any, src LineResolver) error {
	sch, ok := v.compiled[kind]
	if !ok {
		return fmt.Errorf("validate: unknown schema kind %q", kind)
	}

	// Route obj through JSON so the validator sees plain maps/slices with
	// number precision preserved (matches how a loaded file would look).
	raw, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("validate: encoding %s: %w", kind, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("validate: decoding %s: %w", kind, err)
	}

	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return &SchemaError{Kind: kind, src: src, Problems: collectProblems(ve, v.printer, src)}
		}
		return err
	}
	return nil
}

// pointerOf builds an RFC 6901 JSON pointer from instance-location segments.
func pointerOf(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	esc := make([]string, len(segments))
	for i, s := range segments {
		s = strings.ReplaceAll(s, "~", "~0")
		s = strings.ReplaceAll(s, "/", "~1")
		esc[i] = s
	}
	return "/" + strings.Join(esc, "/")
}

// docID reads the "$id" string from a parsed schema document.
func docID(doc any) (string, bool) {
	m, ok := doc.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := m["$id"].(string)
	return id, ok && id != ""
}
