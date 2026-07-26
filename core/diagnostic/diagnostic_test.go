package diagnostic_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/diagnostic"
)

func TestWrapPreservesCauseAndPayload(t *testing.T) {
	sentinel := errors.New("missing file")
	err := diagnostic.Wrap(sentinel, diagnostic.KindTool, "tool_missing_file", "read", "filesystem.read", "file missing", "check the path")
	err = diagnostic.WithRetryAfter(err, 2*time.Second)

	if !errors.Is(err, sentinel) {
		t.Fatalf("wrapped error no longer matches cause")
	}
	payload := diagnostic.Payload(err, "")
	if payload["kind"] != "tool" || payload["code"] != "tool_missing_file" || payload["nodeId"] != "read" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["retryAfterMs"] != int64(2000) {
		t.Fatalf("retryAfterMs = %#v", payload["retryAfterMs"])
	}
}
