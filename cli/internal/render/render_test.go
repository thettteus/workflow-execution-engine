package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

func ev(t domain.EventType, node string, payload map[string]any) domain.Event {
	return domain.Event{Type: t, NodeID: node, Timestamp: time.Unix(0, 0), Payload: payload}
}

// TestJSONRendererEmitsValidEventLines: every line the JSON renderer writes is
// a valid domain.Event — the machine contract (REQ-CLI-03).
func TestJSONRendererEmitsValidEventLines(t *testing.T) {
	var buf bytes.Buffer
	r := JSON(&buf)
	r.Event(ev(domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "1.0.0"}))
	r.Event(ev(domain.WorkerFinished, "a", map[string]any{"costUsd": 0.01, "tokens": float64(5)}))
	r.Finish(&engine.Result{State: domain.ExecutionSucceeded})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 event lines (Finish emits none), got %d: %q", len(lines), buf.String())
	}
	for _, line := range lines {
		var got domain.Event
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line is not valid Event JSON: %q: %v", line, err)
		}
	}
}

// TestHumanRendererShowsBadgeAndRunningCost: the human view surfaces a distinct
// cache-hit badge and a running cost, in plain text (no TTY here, so no color).
func TestHumanRendererShowsBadgeAndRunningCost(t *testing.T) {
	var buf bytes.Buffer
	r := Human(&buf)
	r.Event(ev(domain.CacheHit, "a", map[string]any{"savedCostUsd": 0.02}))
	r.Event(ev(domain.WorkerFinished, "b", map[string]any{"costUsd": 0.03, "tokens": float64(9)}))
	r.Finish(&engine.Result{State: domain.ExecutionSucceeded, Nodes: map[string]engine.NodeOutcome{"a": {}, "b": {}}, SpentCostUSD: 0.03, SpentTokens: 9})

	out := buf.String()
	if !strings.Contains(out, "CACHE HIT") {
		t.Errorf("expected a CACHE HIT badge, got:\n%s", out)
	}
	if !strings.Contains(out, "running $0.0300") {
		t.Errorf("expected a running cost, got:\n%s", out)
	}
	if !strings.Contains(out, "succeeded") {
		t.Errorf("expected the final state, got:\n%s", out)
	}
}
