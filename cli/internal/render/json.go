package render

import (
	"encoding/json"
	"io"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// jsonRenderer emits one JSON object per event to w — line-delimited JSON that
// matches domain.Event's schema exactly (REQ-CLI-03). This is the stream a
// downstream consumer (and, in M1.12, wee serve) parses. Finish emits nothing:
// the ExecutionFinished event already carries the terminal state, so the stream
// stays purely events with no summary line to special-case.
type jsonRenderer struct {
	enc *json.Encoder
}

// JSON returns a Renderer that writes line-delimited event JSON to w.
func JSON(w io.Writer) Renderer {
	return &jsonRenderer{enc: json.NewEncoder(w)}
}

func (r *jsonRenderer) Event(ev domain.Event) { _ = r.enc.Encode(ev) }
func (r *jsonRenderer) Finish(_ *engine.Result) {}
