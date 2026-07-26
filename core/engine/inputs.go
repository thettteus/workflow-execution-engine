package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// ErrMissingInput is returned by resolveWorkflowInputs when a Workflow
// declares a required input (REQ-INPUT-01) that no caller-supplied value and
// no Default can satisfy. Run fails on this before dispatching any node —
// same "before the call, not after" spirit as a Budget check (PRIN-05).
var ErrMissingInput = errors.New("engine: missing required workflow input")

// resolveWorkflowInputs merges a Workflow's declared Inputs with the values a
// caller supplied for this run: supplied values win, a declared Default fills
// the gap otherwise, and a Required declaration satisfied by neither is a
// fatal, pre-dispatch error. Supplied keys the Workflow never declared pass
// through unused — harmless, the same way an unread OS environment variable
// is harmless.
func resolveWorkflowInputs(decls []domain.InputDecl, supplied map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(decls)+len(supplied))
	for k, v := range supplied {
		resolved[k] = v
	}
	var missing []string
	for _, d := range decls {
		if _, ok := resolved[d.Name]; ok {
			continue
		}
		if d.Default != "" {
			resolved[d.Name] = d.Default
			continue
		}
		if d.Required {
			missing = append(missing, d.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: %s", ErrMissingInput, strings.Join(missing, ", "))
	}
	return resolved, nil
}
