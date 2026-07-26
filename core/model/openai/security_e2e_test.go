package openai_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/model/openai"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/security"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// TestNoKeyMaterialInExecutionRecord is NFR-SEC-01, end-to-end: a real provider
// client configured with a secret key drives an execution; nothing the run
// writes to disk — events, snapshot, or artifacts — may contain the key. This
// lives in the provider package because the vendor-type isolation test forbids
// the engine's own tests from importing a concrete provider.
func TestNoKeyMaterialInExecutionRecord(t *testing.T) {
	const secret = "sk-DO-NOT-LEAK-0xC0FFEE"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"score\":1,\"issues\":[]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`)
	}))
	defer srv.Close()

	registry := model.NewRegistry()
	registry.Register("openai", openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey(secret)))

	workers := engine.MapWorkerSource{"reviewer@1.0.0": {
		ID: "reviewer", Version: "1.0.0", Objective: "review",
		Contract: domain.Contract{OutputSchema: map[string]any{
			"type": "object", "required": []any{"score", "issues"},
			"properties": map[string]any{
				"score":  map[string]any{"type": "number"},
				"issues": map[string]any{"type": "array"},
			},
		}},
		Model: domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}}

	base := t.TempDir()
	ex := engine.NewWorkerExecutor(workers, registry)
	s := engine.New(ex, store.New(base), eventlog.New(base), cache.New(base))

	wf := &domain.Workflow{ID: "sec", Version: "1.0.0", Nodes: []domain.Node{{ID: "A", Worker: "reviewer@1.0.0"}}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}

	bundle, err := replay.ExportBundle(eventlog.New(base), store.New(base), "e1")
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "bundle.tar"), bundle, 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	findings, err := security.ScanFilesForSecrets(base, []string{secret})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(findings) > 0 {
		t.Fatalf("API key material leaked into persisted runtime files: %#v", findings)
	}
}
