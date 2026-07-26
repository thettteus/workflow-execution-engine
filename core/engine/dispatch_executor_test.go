package engine_test

import (
	"context"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

func TestDispatchExecutorRoutesByNodeKind(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(0)}
	prov := &fakeProvider{outputs: []string{`{"score":1,"issues":[]}`}}
	we := engine.NewWorkerExecutor(workers, fakeRegistry(prov))

	ft := &fakeTool{name: "fake"}
	te := engine.NewToolExecutor(registryWith(ft))

	d := engine.NewDispatchExecutor(we, te)

	t.Run("worker-backed node routes to WorkerExecutor", func(t *testing.T) {
		res, err := d.Execute(context.Background(), engine.NodeRequest{Node: domain.Node{ID: "a", Worker: "reviewer@1.0.0"}})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !res.Validated {
			t.Errorf("expected the worker path (Validated=true), got %+v", res)
		}
	})

	t.Run("tool-backed node routes to ToolExecutor", func(t *testing.T) {
		node := domain.Node{ID: "b", Tool: &domain.ToolCall{ToolName: "fake", Input: map[string]any{}}}
		res, err := d.Execute(context.Background(), engine.NodeRequest{Node: node})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.Validated {
			t.Errorf("expected the tool path (Validated=false), got %+v", res)
		}
	})
}

func TestDispatchExecutorCacheKeyOnlyForWorkerNodes(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(0)}
	we := engine.NewWorkerExecutor(workers, model.NewRegistry())
	te := engine.NewToolExecutor(tool.NewRegistry())
	d := engine.NewDispatchExecutor(we, te)

	if _, ok := d.CacheKey(domain.Node{ID: "a", Worker: "reviewer@1.0.0"}, nil, nil); !ok {
		t.Error("worker-backed node should produce a cache key")
	}
	if _, ok := d.CacheKey(domain.Node{ID: "b", Tool: &domain.ToolCall{ToolName: "fake"}}, nil, nil); ok {
		t.Error("tool-backed node must never produce a cache key (REQ-WORKER-07)")
	}
}

func TestDispatchExecutorExecuteWithEmitRoutesToolNodeOnly(t *testing.T) {
	workers := engine.MapWorkerSource{"reviewer@1.0.0": scoreWorker(0)}
	prov := &fakeProvider{outputs: []string{`{"score":1,"issues":[]}`}}
	we := engine.NewWorkerExecutor(workers, fakeRegistry(prov))
	te := engine.NewToolExecutor(registryWith(&fakeTool{name: "fake"}))
	d := engine.NewDispatchExecutor(we, te)

	var toolEvents, workerEvents int
	emit := func(t domain.EventType, _ map[string]any) {
		if t == domain.ToolCalled || t == domain.ToolResult {
			toolEvents++
		}
	}

	// Worker-backed: falls back to plain Execute, emits nothing itself.
	if _, err := d.ExecuteWithEmit(context.Background(), engine.NodeRequest{Node: domain.Node{ID: "a", Worker: "reviewer@1.0.0"}}, emit); err != nil {
		t.Fatalf("ExecuteWithEmit (worker): %v", err)
	}
	workerEvents = toolEvents // still 0
	if workerEvents != 0 {
		t.Errorf("worker-backed node should not emit tool events, got %d", workerEvents)
	}

	// Tool-backed: routes through ToolExecutor, emits the ToolCalled/ToolResult pair.
	node := domain.Node{ID: "b", Tool: &domain.ToolCall{ToolName: "fake", Input: map[string]any{}}}
	if _, err := d.ExecuteWithEmit(context.Background(), engine.NodeRequest{Node: node}, emit); err != nil {
		t.Fatalf("ExecuteWithEmit (tool): %v", err)
	}
	if toolEvents != 2 {
		t.Errorf("tool-backed node should emit 2 tool events, got %d", toolEvents)
	}
}
