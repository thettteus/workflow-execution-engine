// Package render turns an execution's event stream into terminal output. Both
// renderers consume the same domain.Event stream the engine writes to the log —
// the CLI never invents a second source of truth (PRIN-02). JSON emits one JSON
// object per line (the contract wee serve will also honor); Human prints a
// readable, incremental status line per node.
package render

import (
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// Renderer consumes an execution's events as they arrive and a final result.
// Event is called in log order, once per event; Finish once at the end.
type Renderer interface {
	Event(ev domain.Event)
	Finish(res *engine.Result)
}
