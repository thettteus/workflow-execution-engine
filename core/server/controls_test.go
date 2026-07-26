package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/settings"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/tool"
	"github.com/thettteus/workflow-execution-engine/core/tool/filesystem"
)

// countingExec counts executions per node and fails a node's FIRST execution
// when failFirst[id] is set (a plain — hence fatal — error, no retry).
type countingExec struct {
	mu        sync.Mutex
	starts    map[string]int
	failFirst map[string]bool
}

func (c *countingExec) Execute(_ context.Context, req engine.NodeRequest) (engine.NodeResult, error) {
	c.mu.Lock()
	c.starts[req.Node.ID]++
	n := c.starts[req.Node.ID]
	ff := c.failFirst[req.Node.ID]
	c.mu.Unlock()
	if ff && n == 1 {
		return engine.NodeResult{}, errors.New("boom")
	}
	return engine.NodeResult{Content: []byte(`{"ok":true}`), Type: domain.ArtifactJSON}, nil
}

func (c *countingExec) count(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts[id]
}

// blockingExec blocks each node until the run's context is cancelled, so a test
// can catch a run mid-flight and cancel it.
type blockingExec struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingExec) Execute(ctx context.Context, _ engine.NodeRequest) (engine.NodeResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return engine.NodeResult{}, ctx.Err()
}

// terminalState returns the state of an execution's most recent
// ExecutionFinished (resume appends a second one), or "" if none yet.
func terminalState(events []domain.Event) string {
	st := ""
	for _, ev := range events {
		if ev.Type == domain.ExecutionFinished {
			if s, ok := ev.Payload["state"].(string); ok {
				st = s
			}
		}
	}
	return st
}

func waitForState(t *testing.T, log *eventlog.Log, id, want string) {
	t.Helper()
	for i := 0; i < 600; i++ {
		if events, err := log.ReadAll(id); err == nil && terminalState(events) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %q on %s", want, id)
}

// sharedAssembler returns an Assembler whose Scheduler always uses the same exec
// and workflow over ws — so a run and its later resume/retry share one counter.
func sharedAssembler(ws string, exec engine.NodeExecutor, wf *domain.Workflow) Assembler {
	return func(string) (*Assembly, error) {
		sched := engine.New(exec, store.New(ws), eventlog.New(ws), cache.New(ws))
		return &Assembly{Scheduler: sched, Workflow: wf}, nil
	}
}

// TestRetryReusesFinishedUpstreamNoDoublePay is M2.2 acceptance #2: after A
// succeeds and B fails, retrying re-runs only B — A is reused from the record,
// never re-executed (REQ-CTRL-04).
func TestRetryReusesFinishedUpstreamNoDoublePay(t *testing.T) {
	ws := t.TempDir()
	exec := &countingExec{starts: map[string]int{}, failFirst: map[string]bool{"B": true}}
	wf := &domain.Workflow{
		ID: "chain", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "A"}, {ID: "B"}},
		Edges: []domain.Edge{{From: "A", To: "B"}},
	}
	s := New(Config{Workspace: ws, Assemble: sharedAssembler(ws, exec, wf), NewID: func(string) string { return "chain-e1" }, DefaultCache: engine.CacheOff})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	log := eventlog.New(ws)

	// Fresh run: A succeeds, B fails → execution failed.
	post(t, ts.URL+"/api/run", `{"workflow":"chain.yaml"}`)
	waitForState(t, log, "chain-e1", string(domain.ExecutionFailed))
	if exec.count("A") != 1 || exec.count("B") != 1 {
		t.Fatalf("after first run: A=%d B=%d, want 1/1", exec.count("A"), exec.count("B"))
	}

	// Retry (resume): A reused (not re-run), B re-runs and now succeeds.
	post(t, ts.URL+"/api/executions/chain-e1/retry", ``)
	waitForState(t, log, "chain-e1", string(domain.ExecutionSucceeded))
	if exec.count("A") != 1 {
		t.Errorf("A re-executed on retry (count=%d) — completed upstream work must be reused", exec.count("A"))
	}
	if exec.count("B") != 2 {
		t.Errorf("B should have re-run exactly once on retry, count=%d", exec.count("B"))
	}
}

// TestCancelInFlightRunFinalizesCancelled: a running execution can be cancelled
// through the API and finalizes as cancelled (REQ-CTRL-03, REQ-RUNTIME-05).
func TestCancelInFlightRunFinalizesCancelled(t *testing.T) {
	ws := t.TempDir()
	exec := &blockingExec{started: make(chan struct{})}
	wf := &domain.Workflow{ID: "blk", Version: "1.0.0", Nodes: []domain.Node{{ID: "a"}}}
	s := New(Config{Workspace: ws, Assemble: sharedAssembler(ws, exec, wf), NewID: func(string) string { return "blk-e1" }, DefaultCache: engine.CacheOff})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post(t, ts.URL+"/api/run", `{"workflow":"blk.yaml"}`)
	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("run never started")
	}
	resp := post(t, ts.URL+"/api/executions/blk-e1/cancel", ``)
	if resp != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want 202", resp)
	}
	waitForState(t, eventlog.New(ws), "blk-e1", string(domain.ExecutionCancelled))
}

// TestCancelNotRunningIs409: cancelling a run that is not in flight is a 409.
func TestCancelNotRunningIs409(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if got := post(t, ts.URL+"/api/executions/nope/cancel", ``); got != http.StatusConflict {
		t.Fatalf("cancel of non-running = %d, want 409", got)
	}
}

func mutationServer(t *testing.T) (*Server, *eventlog.Log, string) {
	t.Helper()
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	wf := &domain.Workflow{
		ID: "mutate", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "write", Tool: &domain.ToolCall{ToolName: "filesystem", Input: map[string]any{
			"op": "write", "path": "out.txt", "content": "ok",
		}}}},
	}
	asm := func(string) (*Assembly, error) {
		tools := tool.NewRegistry()
		tools.Register(filesystem.New(root))
		sched := engine.New(engine.NewToolExecutor(tools), store.New(ws), eventlog.New(ws), cache.New(ws))
		return &Assembly{Scheduler: sched, Workflow: wf}, nil
	}
	return New(Config{Workspace: ws, Assemble: asm, NewID: func(string) string { return "mut-e1" }, DefaultCache: engine.CacheOff}), eventlog.New(ws), root
}

func TestApprovePendingMutationSurvivesRestartAndResumes(t *testing.T) {
	s, log, root := mutationServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post(t, ts.URL+"/api/run", `{"workflow":"mutate.yaml"}`)
	waitForState(t, log, "mut-e1", string(domain.ExecutionPaused))
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file written before approval: %v", err)
	}

	// Simulate a server restart: paused has a terminal ExecutionFinished event,
	// so reconciliation must not cancel or lose the pending checkpoint.
	s2 := New(Config{Workspace: s.workspace, Assemble: s.assemble, NewID: func(string) string { return "mut-e2" }, DefaultCache: engine.CacheOff})
	s2.Reconcile()
	if got := s2.summarize("mut-e1").State; got != string(domain.ExecutionPaused) {
		t.Fatalf("restart reconcile state = %q, want paused", got)
	}
	ts2 := httptest.NewServer(s2.Handler())
	defer ts2.Close()

	resp, err := http.Get(ts2.URL + "/api/executions/mut-e1/approvals")
	if err != nil {
		t.Fatal(err)
	}
	var approvals []Approval
	if err := json.NewDecoder(resp.Body).Decode(&approvals); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(approvals) != 1 || approvals[0].Status != "pending" {
		t.Fatalf("approvals = %+v", approvals)
	}
	checkpoint := approvals[0].CheckpointID
	if got := post(t, ts2.URL+"/api/executions/mut-e1/approvals/"+checkpoint+"/approve", ``); got != http.StatusAccepted {
		t.Fatalf("approve status = %d, want 202", got)
	}
	waitForState(t, log, "mut-e1", string(domain.ExecutionSucceeded))
	if data, err := os.ReadFile(filepath.Join(root, "out.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("approved file = %q err=%v", data, err)
	}

	if got := post(t, ts2.URL+"/api/executions/mut-e1/approvals/"+checkpoint+"/approve", ``); got != http.StatusOK {
		t.Fatalf("duplicate approve status = %d, want 200", got)
	}
	events, _ := log.ReadAll("mut-e1")
	grants := 0
	for _, ev := range events {
		if ev.Type == domain.ApprovalGranted {
			grants++
		}
	}
	if grants != 1 {
		t.Fatalf("ApprovalGranted count = %d, want 1", grants)
	}
}

func TestRejectPendingMutationFailsWithoutToolCalled(t *testing.T) {
	s, log, root := mutationServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	post(t, ts.URL+"/api/run", `{"workflow":"mutate.yaml"}`)
	waitForState(t, log, "mut-e1", string(domain.ExecutionPaused))
	approvals, _ := s.approvals("mut-e1")
	var checkpoint string
	for id := range approvals {
		checkpoint = id
	}
	if got := post(t, ts.URL+"/api/executions/mut-e1/approvals/"+checkpoint+"/reject", ``); got != http.StatusAccepted {
		t.Fatalf("reject status = %d, want 202", got)
	}
	waitForState(t, log, "mut-e1", string(domain.ExecutionFailed))
	events, _ := log.ReadAll("mut-e1")
	for _, ev := range events {
		if ev.Type == domain.ToolCalled {
			t.Fatal("ToolCalled emitted after rejection")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file written despite rejection: %v", err)
	}
}

func TestStaleApprovalCheckpointIsConflict(t *testing.T) {
	s, _, _ := mutationServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if got := post(t, ts.URL+"/api/executions/mut-e1/approvals/nope/approve", ``); got != http.StatusConflict {
		t.Fatalf("stale approve status = %d, want 409", got)
	}
}

// TestReconcileMarksInterruptedRunCancelled is M2.2 acceptance #1's core: a run
// left with ExecutionStarted but no terminal event (a crashed process) is
// settled to cancelled on Reconcile — never left reported as running — and stays
// resumable (REQ-CTRL-02).
func TestReconcileMarksInterruptedRunCancelled(t *testing.T) {
	ws := t.TempDir()
	log := eventlog.New(ws)
	const id = "orphan-20260721T000000-aaaa"
	seed(t, log, id)
	appendEvent(t, log, id, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "1.0.0"})
	appendEvent(t, log, id, domain.WorkerFinished, "a", map[string]any{"costUsd": 0.01})
	// no terminal event — the process "died" here.

	s := New(Config{Workspace: ws})
	// Before reconcile the list still derives "running" (no terminal event).
	if got := s.summarize(id).State; got != "running" {
		t.Fatalf("pre-reconcile state = %q, want running", got)
	}
	s.Reconcile()
	if got := s.summarize(id).State; got != string(domain.ExecutionCancelled) {
		t.Fatalf("post-reconcile state = %q, want cancelled", got)
	}
	// Reconcile is idempotent: a second pass adds nothing (already terminal).
	before, _ := log.ReadAll(id)
	s.Reconcile()
	after, _ := log.ReadAll(id)
	if len(after) != len(before) {
		t.Fatalf("reconcile not idempotent: %d → %d events", len(before), len(after))
	}
}

// TestDeriveProgress: completion counts and running set are folded from events
// (REQ-CTRL-06), with no dependency on any persisted progress record.
func TestDeriveProgress(t *testing.T) {
	events := []domain.Event{
		{Type: domain.ExecutionStarted, Timestamp: time.Unix(1, 0)},
		{Type: domain.WorkerStarted, NodeID: "a", Timestamp: time.Unix(2, 0)},
		{Type: domain.WorkerFinished, NodeID: "a", Timestamp: time.Unix(3, 0)},
		{Type: domain.WorkerStarted, NodeID: "b", Timestamp: time.Unix(4, 0)},
	}
	p := deriveProgress(events, 3)
	if p.TotalNodes != 3 || p.CompletedNodes != 1 {
		t.Fatalf("total/completed = %d/%d, want 3/1", p.TotalNodes, p.CompletedNodes)
	}
	if len(p.RunningNodes) != 1 || p.RunningNodes[0] != "b" {
		t.Fatalf("running = %v, want [b]", p.RunningNodes)
	}
	if p.LastEventUnixMs != time.Unix(4, 0).UnixMilli() {
		t.Fatalf("lastEvent = %d, want %d", p.LastEventUnixMs, time.Unix(4, 0).UnixMilli())
	}
}

// TestCacheClearByExecutionNode resolves a node's recorded key and clears only
// it (REQ-CTRL-03), leaving unrelated entries intact.
func TestCacheClearByExecutionNode(t *testing.T) {
	ws := t.TempDir()
	s := New(Config{Workspace: ws})
	if err := s.cache.Put(cache.Entry{Key: "keyA"}); err != nil {
		t.Fatal(err)
	}
	if err := s.cache.Put(cache.Entry{Key: "keyB"}); err != nil {
		t.Fatal(err)
	}
	log := eventlog.New(ws)
	const id = "run-1"
	seed(t, log, id)
	appendEvent(t, log, id, domain.CacheMiss, "a", map[string]any{"key": "keyA"})
	appendEvent(t, log, id, domain.CacheMiss, "b", map[string]any{"key": "keyB"})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	post(t, ts.URL+"/api/cache/clear", `{"executionId":"run-1","nodeId":"a"}`)

	if _, ok := s.cache.Get("keyA"); ok {
		t.Error("node a's cache key should be cleared")
	}
	if _, ok := s.cache.Get("keyB"); !ok {
		t.Error("node b's cache key must remain")
	}
}

// TestSettingsRoundTripSurvivesRestart is M2.2 acceptance #1 for settings: PUT
// then GET returns them, and a fresh Server over the same workspace (a restart)
// still returns them (REQ-CTRL-05).
func TestSettingsRoundTripSurvivesRestart(t *testing.T) {
	ws := t.TempDir()
	s := New(Config{Workspace: ws})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	post2(t, http.MethodPut, ts.URL+"/api/settings", `{"cacheMode":"readonly","defaultBudgetUsd":3,"providerKeyEnv":{"openai":"OPENAI_API_KEY"}}`)

	// A brand-new server (restart) over the same workspace still has them.
	s2 := New(Config{Workspace: ws})
	ts2 := httptest.NewServer(s2.Handler())
	defer ts2.Close()
	resp, err := http.Get(ts2.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["cacheMode"] != "readonly" || got["defaultBudgetUsd"].(float64) != 3 {
		t.Fatalf("settings not durable across restart: %+v", got)
	}
}

// TestBundleDownloadReturnsTar: GET .../bundle returns a tar attachment
// (REQ-CTRL-03); the archive contents are unit-tested in core/replay.
func TestBundleDownloadReturnsTar(t *testing.T) {
	ws := t.TempDir()
	log := eventlog.New(ws)
	st := store.New(ws)
	hash, _ := st.Put([]byte("art"))
	if err := log.WriteSnapshot("run-b", engine.Snapshot{Workflow: domain.Workflow{ID: "wf", Version: "1.0.0"}}); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, log, "run-b", domain.ArtifactCreated, "a", map[string]any{"hash": hash, "type": "json"})
	appendEvent(t, log, "run-b", domain.ExecutionFinished, "", map[string]any{"state": "succeeded"})

	s := New(Config{Workspace: ws})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/executions/run-b/bundle")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bundle status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("content-type = %q, want application/x-tar", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "run-b.tar") {
		t.Errorf("content-disposition = %q, want filename run-b.tar", cd)
	}
}

// TestEffectiveCacheAndBudgetPrecedence: per-run overrides win, then persisted
// settings, then the server/workflow defaults (REQ-CTRL-07).
func TestEffectiveCacheAndBudgetPrecedence(t *testing.T) {
	ws := t.TempDir()
	s := New(Config{Workspace: ws, DefaultCache: engine.CacheOff})

	// No settings, no request value → server default.
	if got := s.effectiveCache(""); got != engine.CacheOff {
		t.Errorf("default cache = %q, want off", got)
	}
	// An explicit request value wins over the default.
	if got := s.effectiveCache("on"); got != engine.CacheOn {
		t.Errorf("request cache = %q, want on", got)
	}

	// Persisted settings override the server default when the request is empty.
	if err := s.settings.Save(settings.Settings{CacheMode: "readonly", DefaultBudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	if got := s.effectiveCache(""); got != engine.CacheReadOnly {
		t.Errorf("settings cache = %q, want readonly", got)
	}

	wf := &domain.Workflow{Budget: domain.Budget{MaxCostUSD: 2}}
	if b := s.effectiveBudget(wf, 9); b.MaxCostUSD != 9 {
		t.Errorf("override budget = %v, want 9", b.MaxCostUSD)
	}
	if b := s.effectiveBudget(wf, 0); b.MaxCostUSD != 2 {
		t.Errorf("workflow budget = %v, want its own 2", b.MaxCostUSD)
	}
	// A workflow with no cost cap of its own backfills from the persisted default.
	if b := s.effectiveBudget(&domain.Workflow{}, 0); b.MaxCostUSD != 4 {
		t.Errorf("backfilled budget = %v, want settings default 4", b.MaxCostUSD)
	}
}

// post POSTs a JSON body and returns the status code.
func post(t *testing.T, url, body string) int {
	t.Helper()
	return post2(t, http.MethodPost, url, body)
}

func post2(t *testing.T, method, url, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
