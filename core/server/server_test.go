package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/core/settings"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// writeSnapshot seeds a minimal snapshot so Append can chain the first event to
// it (Append reads the snapshot hash as the chain root).
func seed(t *testing.T, log *eventlog.Log, id string) {
	t.Helper()
	if err := log.WriteSnapshot(id, map[string]any{"id": id}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
}

func appendEvent(t *testing.T, log *eventlog.Log, id string, typ domain.EventType, node string, payload map[string]any) {
	t.Helper()
	if err := log.Append(id, domain.Event{Type: typ, ExecutionID: id, NodeID: node, Payload: payload}); err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
}

// fastServer builds a Server over a temp workspace with a short poll so live
// tests don't wait 40ms per tick. A nil assemble leaves the run controls
// disabled (a read-only server) — what most read-side tests want.
func fastServer(t *testing.T, assemble Assembler) (*Server, *eventlog.Log) {
	t.Helper()
	ws := t.TempDir()
	s := New(Config{Workspace: ws, Assemble: assemble})
	s.poll = 2 * time.Millisecond
	return s, eventlog.New(ws)
}

func TestServesStaticUIWithSPAFallback(t *testing.T) {
	uiDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<main>wee ui</main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "app.js"), []byte("console.log('wee')"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Workspace: t.TempDir(), UIDir: uiDir})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, path := range []string{"/", "/runs/e1"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "wee ui") {
			t.Fatalf("GET %s = %d %q, want index fallback", path, resp.StatusCode, data)
		}
	}
	resp, err := http.Get(ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "console.log") {
		t.Fatalf("asset response = %d %q", resp.StatusCode, data)
	}
	resp, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health through UI server = %d", resp.StatusCode)
	}
}

// stubExec is a NodeExecutor that returns a fixed JSON artifact without a model
// or tool — enough to drive a run to completion through the real Scheduler.
type stubExec struct{}

func (stubExec) Execute(_ context.Context, _ engine.NodeRequest) (engine.NodeResult, error) {
	return engine.NodeResult{Content: []byte(`{"ok":true}`), Type: domain.ArtifactJSON}, nil
}

// waitForEvent polls an execution's log until an event of typ appears (or fails
// the test). Used by control-plane tests that launch a background run and need
// to observe its recorded outcome.
func waitForEvent(t *testing.T, log *eventlog.Log, id string, typ domain.EventType) {
	t.Helper()
	for i := 0; i < 400; i++ {
		events, err := log.ReadAll(id)
		if err == nil {
			for _, ev := range events {
				if ev.Type == typ {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s on %s", typ, id)
}

// fastServerWithTemplates is fastServer plus a Dir (a fresh temp dir, for
// handleImportTemplate's unpacked files) and the given TemplatesDir.
func fastServerWithTemplates(t *testing.T, templatesDir string) (*Server, string) {
	t.Helper()
	ws := t.TempDir()
	dir := t.TempDir()
	s := New(Config{Workspace: ws, Dir: dir, TemplatesDir: templatesDir})
	s.poll = 2 * time.Millisecond
	return s, dir
}

func TestEventsReplaysThenClosesOnTerminal(t *testing.T) {
	s, log := fastServer(t, nil)
	const id = "wf-20260718T000000-aaaa"
	seed(t, log, id)
	// A complete run recorded before the client connects: the stream must
	// replay all of it and then close on ExecutionFinished.
	appendEvent(t, log, id, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "1.0.0"})
	appendEvent(t, log, id, domain.WorkerStarted, "a", nil)
	appendEvent(t, log, id, domain.WorkerFinished, "a", map[string]any{"costUsd": 0.01, "tokens": 5})
	appendEvent(t, log, id, domain.ExecutionFinished, "", map[string]any{"state": "succeeded"})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	events := readWS(t, ts.URL+"/api/executions/"+id+"/events", 2*time.Second)
	if len(events) != 4 {
		t.Fatalf("want 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != domain.ExecutionStarted || events[3].Type != domain.ExecutionFinished {
		t.Fatalf("unexpected order: %s ... %s", events[0].Type, events[3].Type)
	}
}

func TestEventsTailsLiveEventsAppendedAfterConnect(t *testing.T) {
	s, log := fastServer(t, nil)
	const id = "wf-20260718T000001-bbbb"
	seed(t, log, id)
	appendEvent(t, log, id, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "1.0.0"})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Connect while the run is still "in flight", then append the rest.
	got := make(chan []domain.Event, 1)
	go func() { got <- readWS(t, ts.URL+"/api/executions/"+id+"/events", 2*time.Second) }()

	time.Sleep(20 * time.Millisecond) // let the stream open and drain the first event
	appendEvent(t, log, id, domain.WorkerStarted, "a", nil)
	appendEvent(t, log, id, domain.ArtifactCreated, "a", map[string]any{"hash": "deadbeef", "type": "json"})
	appendEvent(t, log, id, domain.ExecutionFinished, "", map[string]any{"state": "succeeded"})

	select {
	case events := <-got:
		if len(events) != 4 {
			t.Fatalf("want 4 events (1 replayed + 3 live), got %d", len(events))
		}
		if events[len(events)-1].Type != domain.ExecutionFinished {
			t.Fatalf("stream did not close on terminal event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live stream never terminated")
	}
}

func TestListAndAudit(t *testing.T) {
	s, log := fastServer(t, nil)
	const done = "wf-20260718T000002-cccc"
	const live = "wf-20260718T000003-dddd"
	if err := log.WriteSnapshot(done, engine.Snapshot{Workflow: domain.Workflow{ID: "wf", Version: "2.0.0"}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	appendEvent(t, log, done, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "2.0.0"})
	appendEvent(t, log, done, domain.WorkerFinished, "a", map[string]any{"costUsd": 0.03, "tokens": 100.0})
	appendEvent(t, log, done, domain.WorkerFinished, "b", map[string]any{"costUsd": 0.02, "tokens": 50.0})
	appendEvent(t, log, done, domain.ExecutionFinished, "", map[string]any{"state": "succeeded"})
	seed(t, log, live)
	appendEvent(t, log, live, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "2.0.0"})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var list []ExecutionSummary
	getJSON(t, ts.URL+"/api/executions", &list)
	if len(list) != 2 {
		t.Fatalf("want 2 executions, got %d", len(list))
	}
	states := map[string]string{}
	summaries := map[string]ExecutionSummary{}
	for _, e := range list {
		states[e.ID] = e.State
		summaries[e.ID] = e
	}
	if states[done] != "succeeded" {
		t.Errorf("finished run state = %q, want succeeded", states[done])
	}
	if states[live] != "running" {
		t.Errorf("in-flight run state = %q, want running", states[live])
	}
	// M1.14: the history table's cost/tokens/duration columns (REQ-METRIC-01/02)
	// summed across both WorkerFinished events, cheap from the event log alone.
	if got := summaries[done]; got.SpentCostUSD != 0.05 || got.SpentTokens != 150 {
		t.Errorf("done summary cost/tokens = %v/%v, want 0.05/150", got.SpentCostUSD, got.SpentTokens)
	}
	if summaries[done].DurationMs < 0 {
		t.Errorf("done summary DurationMs = %d, want >= 0", summaries[done].DurationMs)
	}
	if got := summaries[live].DurationMs; got != 0 {
		t.Errorf("in-flight run DurationMs = %d, want 0 (no ExecutionFinished yet)", got)
	}

	var audit Audit
	getJSON(t, ts.URL+"/api/executions/"+done, &audit)
	if audit.Workflow.Version != "2.0.0" || len(audit.Events) != 4 {
		t.Fatalf("audit mismatch: version=%q events=%d", audit.Workflow.Version, len(audit.Events))
	}
	if audit.State != "succeeded" {
		t.Errorf("audit.State = %q, want succeeded", audit.State)
	}
}

func TestAuditUnknownExecutionIs404(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/executions/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestAuditExposesWorkflowAndWorkers is the wire-format contract the Inspector
// (M1.13, REQ-UI-03) relies on: GET /api/executions/{id} must carry the frozen
// Workflow (so a node's worker ref resolves) and the pinned Workers (so its
// Contract/goal render) — not just the flat event stream M1.12 shipped.
func TestAuditExposesWorkflowAndWorkers(t *testing.T) {
	s, log := fastServer(t, nil)
	const id = "wf-20260719T000004-eeee"

	worker := domain.Worker{ID: "reviewer", Version: "1.0.0", Objective: "review a diff"}
	snap := engine.Snapshot{
		Workflow: domain.Workflow{
			ID:      "wf",
			Version: "1.0.0",
			Nodes:   []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}},
		},
		Workers: map[string]domain.Worker{"reviewer@1.0.0": worker},
	}
	if err := log.WriteSnapshot(id, snap); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	appendEvent(t, log, id, domain.ExecutionStarted, "", map[string]any{"workflow": "wf", "version": "1.0.0"})
	appendEvent(t, log, id, domain.ExecutionFinished, "", map[string]any{"state": "succeeded"})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var audit Audit
	getJSON(t, ts.URL+"/api/executions/"+id, &audit)

	if len(audit.Workflow.Nodes) != 1 || audit.Workflow.Nodes[0].ID != "review" {
		t.Fatalf("audit.Workflow.Nodes = %+v, want the pinned review node", audit.Workflow.Nodes)
	}
	got, ok := audit.Workers["reviewer@1.0.0"]
	if !ok || got.Objective != "review a diff" {
		t.Fatalf("audit.Workers[reviewer@1.0.0] = %+v, ok=%v, want the pinned reviewer Worker", got, ok)
	}
}

// TestRunStartsExecutionAndReturnsID: POST /api/run assembles the workflow,
// mints an id, runs it in the background to completion, and persists run params
// (REQ-CTRL-03/07). The Assembler receives the ref and the server owns the run.
func TestRunStartsExecutionAndReturnsID(t *testing.T) {
	ws := t.TempDir()
	var mu sync.Mutex
	var gotRef string
	asm := func(ref string) (*Assembly, error) {
		mu.Lock()
		gotRef = ref
		mu.Unlock()
		sched := engine.New(stubExec{}, store.New(ws), eventlog.New(ws), cache.New(ws))
		wf := &domain.Workflow{ID: "hello", Version: "1.0.0", Nodes: []domain.Node{{ID: "a"}}}
		return &Assembly{Scheduler: sched, Workflow: wf}, nil
	}
	s := New(Config{Workspace: ws, Assemble: asm, NewID: func(id string) string { return id + "-exec1" }})
	s.poll = 2 * time.Millisecond
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"workflow":"examples/hello.yaml","inputs":{"prUrl":"https://example.com/42"}}`)
	resp, err := http.Post(ts.URL+"/api/run", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out runResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ExecutionID != "hello-exec1" {
		t.Fatalf("executionId = %q, want hello-exec1", out.ExecutionID)
	}

	log := eventlog.New(ws)
	waitForEvent(t, log, out.ExecutionID, domain.ExecutionFinished)

	mu.Lock()
	if gotRef != "examples/hello.yaml" {
		t.Errorf("assembler got ref %q, want examples/hello.yaml", gotRef)
	}
	mu.Unlock()

	// Run params are persisted (so the run can later be resumed/retried), with
	// the caller's inputs — and no secret (there are none here to leak).
	rp, err := s.readRunParams(out.ExecutionID)
	if err != nil {
		t.Fatalf("read run params: %v", err)
	}
	if rp.Workflow != "examples/hello.yaml" || rp.Inputs["prUrl"] != "https://example.com/42" {
		t.Errorf("run params = %+v, want workflow+inputs recorded", rp)
	}
}

func TestRunRecordsConnectionRefsWithoutSecretValues(t *testing.T) {
	ws := t.TempDir()
	const secret = "ghp-should-never-hit-disk"
	t.Setenv("GITHUB_AUTH_HEADER", secret)
	asm := func(string) (*Assembly, error) {
		sched := engine.New(stubExec{}, store.New(ws), eventlog.New(ws), cache.New(ws))
		wf := &domain.Workflow{ID: "connected", Version: "1.0.0", Nodes: []domain.Node{{ID: "a"}}}
		return &Assembly{
			Scheduler: sched,
			Workflow:  wf,
			ConnectionRefs: map[string]engine.ConnectionRef{
				"kimi": {
					ID:        "kimi",
					Label:     "Kimi",
					Kind:      string(settings.ConnectionKindModelProvider),
					Type:      "openai-compatible",
					BaseURL:   "https://api.moonshot.ai/v1",
					SecretEnv: "MOONSHOT_API_KEY",
				},
			},
		}, nil
	}
	s := New(Config{Workspace: ws, Assemble: asm, NewID: func(string) string { return "connected-1" }})
	if err := s.settings.Save(settings.Settings{Connections: []settings.Connection{{
		ID:        "github",
		Label:     "GitHub",
		Kind:      settings.ConnectionKindChangeSource,
		Type:      "github",
		BaseURL:   "https://api.github.com",
		SecretEnv: "GITHUB_AUTH_HEADER",
	}}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/run", "application/json", strings.NewReader(`{"workflow":"connected.yaml","connections":["github"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	waitForEvent(t, eventlog.New(ws), "connected-1", domain.ExecutionFinished)

	var snap engine.Snapshot
	if err := eventlog.New(ws).ReadSnapshot("connected-1", &snap); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap.ConnectionRefs["kimi"].BaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("provider connection ref missing: %+v", snap.ConnectionRefs)
	}
	if snap.ConnectionRefs["github"].SecretEnv != "GITHUB_AUTH_HEADER" {
		t.Fatalf("source connection secret ref = %+v, want env-name only", snap.ConnectionRefs["github"])
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("snapshot leaked secret value:\n%s", data)
	}
}

func TestNoForgeNamedPackagesUnderCore(t *testing.T) {
	forbidden := map[string]bool{"github": true, "gitlab": true, "bitbucket": true}
	root := filepath.Clean("../")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if forbidden[name] {
			t.Fatalf("forge-specific core package/directory found: %s", path)
		}
		if name == ".git" || name == "testdata" {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk core: %v", err)
	}
}

func TestRunDisabledWithoutAssembler(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/run", "application/json", strings.NewReader(`{"workflow":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
}

func TestCORSPreflight(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/settings", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPut)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q, want the echoed loopback UI origin", got)
	}
	methods := resp.Header.Get("Access-Control-Allow-Methods")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		if !strings.Contains(methods, method) {
			t.Fatalf("allow-methods = %q, missing %s", methods, method)
		}
	}
}

// readWS dials the WebSocket events endpoint and collects domain.Events from
// each text frame until the connection closes (the server closes cleanly on
// ExecutionFinished) or the deadline hits.
func readWS(t *testing.T, url string, deadline time.Duration) []domain.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(url, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer func() { _ = conn.CloseNow() }()

	var events []domain.Event
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return events // server closed the connection (or the deadline hit)
		}
		var ev domain.Event
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("bad frame %q: %v", data, err)
		}
		events = append(events, ev)
	}
}

func getJSON(t *testing.T, url string, dst any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

// testBundle builds a minimal, real `wee export`-shaped bundle in-memory (one
// workflow, one worker) — the M1.14 template tests' fixture, so they don't
// depend on the repo's actual examples/templates/*.tar files.
func testBundle(t *testing.T) []byte {
	t.Helper()
	reg := registry.New()
	w := domain.Worker{
		ID: "reviewer", Version: "1.0.0", Objective: "review",
		Constraints: []string{}, Tools: []string{},
		ContextPolicy: domain.ContextPolicy{Mode: domain.ContextDiffOnly},
		Contract:      domain.Contract{Goal: "g", OutputSchema: map[string]any{"type": "object"}},
		Model:         domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}
	if err := reg.RegisterWorker(w); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	wf := domain.Workflow{
		ID: "demo-template", Version: "1.0.0",
		Nodes: []domain.Node{{ID: "review", Worker: "reviewer@1.0.0"}},
	}
	if err := reg.RegisterWorkflow(wf); err != nil {
		t.Fatalf("RegisterWorkflow: %v", err)
	}
	bundle, err := reg.Export("demo-template", "1.0.0")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return bundle
}

func TestListTemplatesEmptyWhenNotConfigured(t *testing.T) {
	s, _ := fastServer(t, nil) // TemplatesDir unset
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var list []Template
	getJSON(t, ts.URL+"/api/templates", &list)
	if list == nil || len(list) != 0 {
		t.Fatalf("templates = %+v, want an empty (not nil) list", list)
	}
}

func TestListTemplatesReadsBundlesFromTemplatesDir(t *testing.T) {
	templatesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(templatesDir, "demo.tar"), testBundle(t), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	s, _ := fastServerWithTemplates(t, templatesDir)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var list []Template
	getJSON(t, ts.URL+"/api/templates", &list)
	if len(list) != 1 {
		t.Fatalf("templates = %+v, want exactly one", list)
	}
	got := list[0]
	if got.Name != "demo" || got.WorkflowID != "demo-template" || got.Version != "1.0.0" || got.NodeCount != 1 {
		t.Errorf("template = %+v, want name=demo workflowId=demo-template version=1.0.0 nodeCount=1", got)
	}
	// M2.3: the row also carries registry.DeriveTemplateFacts(wf) — a
	// worker-only (no tool) node must read as read-only with no tools.
	if got.WriteCapable {
		t.Error("WriteCapable = true, want false for a worker-only node")
	}
	if len(got.Tools) != 0 {
		t.Errorf("Tools = %v, want empty for a worker-only node", got.Tools)
	}
	if len(got.RequiredConnections) != 1 || got.RequiredConnections[0] != "openai" {
		t.Errorf("RequiredConnections = %v, want [openai]", got.RequiredConnections)
	}
}

func TestImportTemplateWritesRunnableFilesAndReturnsWorkflow(t *testing.T) {
	templatesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(templatesDir, "demo.tar"), testBundle(t), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	s, dir := fastServerWithTemplates(t, templatesDir)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/templates/demo/import", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST import = %d: %s", resp.StatusCode, b)
	}
	var got importTemplateResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantWorkflowPath, err := filepath.Rel(dir, filepath.Join(s.workspace, "imported-templates", "demo", "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkflowPath != wantWorkflowPath {
		t.Errorf("WorkflowPath = %q, want %s", got.WorkflowPath, wantWorkflowPath)
	}
	if got.Workflow.ID != "demo-template" {
		t.Errorf("Workflow.ID = %q, want demo-template", got.Workflow.ID)
	}

	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("import should not create %s; stat err=%v", filepath.Join(dir, "demo"), err)
	}
	// The files must actually exist as real YAML, real enough for wee run to
	// resolve through the returned relative path — the whole point of unpacking
	// rather than inventing an in-memory-only execution path for templates.
	wfBytes, err := os.ReadFile(filepath.Join(dir, got.WorkflowPath))
	if err != nil {
		t.Fatalf("workflow.yaml was not written: %v", err)
	}
	if !strings.Contains(string(wfBytes), "demo-template") {
		t.Errorf("workflow.yaml doesn't mention the workflow id:\n%s", wfBytes)
	}
	workerBytes, err := os.ReadFile(filepath.Join(s.workspace, "imported-templates", "demo", "reviewer.worker.yaml"))
	if err != nil {
		t.Fatalf("reviewer.worker.yaml was not written: %v", err)
	}
	if !strings.Contains(string(workerBytes), "reviewer") {
		t.Errorf("reviewer.worker.yaml doesn't mention the worker id:\n%s", workerBytes)
	}
}

func TestImportTemplateCopiesSidecarToolConfig(t *testing.T) {
	root := t.TempDir()
	templatesDir := filepath.Join(root, "templates")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templatesDir, "demo.tar"), testBundle(t), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	exampleDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(exampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cfg = "workspaceRoot: .\nhttp:\n  allow: [api.github.com]\n"
	if err := os.WriteFile(filepath.Join(exampleDir, "wee.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write sidecar config: %v", err)
	}
	s, dir := fastServerWithTemplates(t, templatesDir)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/templates/demo/import", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST import = %d: %s", resp.StatusCode, b)
	}

	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("import should not create %s; stat err=%v", filepath.Join(dir, "demo"), err)
	}
	got, err := os.ReadFile(filepath.Join(s.workspace, "imported-templates", "demo", "wee.yaml"))
	if err != nil {
		t.Fatalf("wee.yaml was not copied: %v", err)
	}
	if string(got) != cfg {
		t.Errorf("wee.yaml = %q, want %q", got, cfg)
	}
}

func TestImportTemplateUnknownNameIs404(t *testing.T) {
	s, _ := fastServerWithTemplates(t, t.TempDir())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/templates/does-not-exist/import", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestImportTemplateDisabledWithoutTemplatesDir(t *testing.T) {
	s, _ := fastServer(t, nil) // TemplatesDir unset
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/templates/demo/import", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// demoWorker is schema-complete (M2.3: handleSaveWorker now validates against
// worker.schema.json, whose additionalProperties:false/required list rejects
// a Worker with nil constraints/tools/rules/successCriteria or an unset
// ContextPolicy.Mode) — every field a real WorkerEditor.tsx save would send.
func demoWorker(id, version string) domain.Worker {
	return domain.Worker{
		ID:          id,
		Version:     version,
		Objective:   "review code",
		Constraints: []string{},
		Tools:       []string{},
		ContextPolicy: domain.ContextPolicy{
			Mode: domain.ContextDiffOnly,
		},
		Contract: domain.Contract{
			Goal:            "produce a verdict",
			Rules:           []string{},
			SuccessCriteria: []string{},
			MaxRetries:      1,
			OutputSchema:    map[string]any{"type": "object"},
		},
		Model: domain.ModelConfig{Provider: "openai", Model: "gpt-4o-mini"},
	}
}

func writeWorkerFile(t *testing.T, dir, fileName string, w domain.Worker) {
	t.Helper()
	data, err := serialize.MarshalYAML(w)
	if err != nil {
		t.Fatalf("marshal worker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", fileName, err)
	}
}

func TestListWorkerVersionsEmptyWhenNoneExist(t *testing.T) {
	s, dir := fastServerWithTemplates(t, "")
	_ = dir
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/workers/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got workerVersionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Versions) != 0 {
		t.Errorf("Versions = %+v, want empty", got.Versions)
	}
}

func TestListWorkerVersionsReturnsAllMatchingIDSortedOldestFirst(t *testing.T) {
	s, dir := fastServerWithTemplates(t, "")
	writeWorkerFile(t, dir, "reviewer@1.0.1.worker.yaml", demoWorker("reviewer", "1.0.1"))
	writeWorkerFile(t, dir, "reviewer.worker.yaml", demoWorker("reviewer", "1.0.0"))
	writeWorkerFile(t, dir, "other.worker.yaml", demoWorker("other", "1.0.0")) // different id, must not appear
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/workers/reviewer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got workerVersionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Versions) != 2 {
		t.Fatalf("Versions = %+v, want 2 entries", got.Versions)
	}
	if got.Versions[0].Version != "1.0.0" || got.Versions[1].Version != "1.0.1" {
		t.Errorf("Versions = [%s, %s], want [1.0.0, 1.0.1]", got.Versions[0].Version, got.Versions[1].Version)
	}
}

func TestSaveWorkerCreatesNewVersionFileWithoutTouchingOriginal(t *testing.T) {
	s, dir := fastServerWithTemplates(t, "")
	writeWorkerFile(t, dir, "reviewer.worker.yaml", demoWorker("reviewer", "1.0.0"))
	originalBytes, err := os.ReadFile(filepath.Join(dir, "reviewer.worker.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	edited := demoWorker("reviewer", "whatever-the-client-sent") // server must ignore this
	edited.Objective = "review code more strictly"
	body, _ := json.Marshal(saveWorkerRequest{Worker: edited})
	resp, err := http.Post(ts.URL+"/api/workers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/workers = %d: %s", resp.StatusCode, b)
	}
	var got saveWorkerResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Worker.Version != "1.0.1" {
		t.Errorf("saved Version = %q, want 1.0.1 (server-computed, ignoring the client's submitted version)", got.Worker.Version)
	}

	// The original file is untouched, byte for byte.
	stillThere, err := os.ReadFile(filepath.Join(dir, "reviewer.worker.yaml"))
	if err != nil {
		t.Fatalf("original file missing: %v", err)
	}
	if string(stillThere) != string(originalBytes) {
		t.Errorf("original reviewer.worker.yaml was modified, want byte-identical")
	}

	// The new version's own file exists and round-trips the edited content.
	newFile := filepath.Join(dir, "reviewer@1.0.1.worker.yaml")
	newData, err := os.ReadFile(newFile)
	if err != nil {
		t.Fatalf("new version file missing: %v", err)
	}
	var newWorker domain.Worker
	if err := serialize.UnmarshalYAML(newData, &newWorker); err != nil {
		t.Fatalf("decode new file: %v", err)
	}
	if newWorker.Objective != "review code more strictly" {
		t.Errorf("new file Objective = %q, want the edited text", newWorker.Objective)
	}

	// Both versions coexist and are independently loadable by id — proving
	// rollback needs no engine change (LoadWorkers matches by internal
	// id/version, not filename).
	all, err := scanWorkerVersions(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("scanWorkerVersions found %d versions, want 2", len(all))
	}
}

func TestSaveWorkerRequiresID(t *testing.T) {
	s, _ := fastServerWithTemplates(t, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(saveWorkerRequest{Worker: domain.Worker{}})
	resp, err := http.Post(ts.URL+"/api/workers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestSaveWorkerRejectsSchemaInvalidWorker covers M2.3's server-side
// validation gate: a Worker missing required fields (worker.schema.json)
// must be rejected before it ever reaches disk, not merely on the client.
func TestSaveWorkerRejectsSchemaInvalidWorker(t *testing.T) {
	s, dir := fastServerWithTemplates(t, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	invalid := demoWorker("reviewer", "1.0.0")
	invalid.ContextPolicy = domain.ContextPolicy{} // Mode "" is not one of the enum values
	body, _ := json.Marshal(saveWorkerRequest{Worker: invalid})
	resp, err := http.Post(ts.URL+"/api/workers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer@1.0.0.worker.yaml")); !os.IsNotExist(err) {
		t.Error("an invalid Worker must never reach disk")
	}
}

// TestSaveWorkerRejectsUncompilableOutputSchema covers the check
// Validate(KindWorker) structurally cannot make: worker.schema.json only
// requires outputSchema to be *some* object, so a syntactically-fine but
// non-compilable JSON Schema needs the separate validate.CompileSchema call.
func TestSaveWorkerRejectsUncompilableOutputSchema(t *testing.T) {
	s, dir := fastServerWithTemplates(t, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	invalid := demoWorker("reviewer", "1.0.0")
	invalid.Contract.OutputSchema = map[string]any{"type": "not-a-real-json-schema-type"}
	body, _ := json.Marshal(saveWorkerRequest{Worker: invalid})
	resp, err := http.Post(ts.URL+"/api/workers", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer@1.0.0.worker.yaml")); !os.IsNotExist(err) {
		t.Error("a Worker with a non-compilable output schema must never reach disk")
	}
}

func TestNextPatchVersionStartsAtOneZeroZeroWhenNoneExist(t *testing.T) {
	if got := nextPatchVersion(nil); got != "1.0.0" {
		t.Errorf("nextPatchVersion(nil) = %q, want 1.0.0", got)
	}
}

func TestSecretsStatusReportsPresenceNotValue(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	const name = "WEE_TEST_SECRET_STATUS"
	os.Unsetenv(name)
	t.Cleanup(func() { os.Unsetenv(name) })
	os.Setenv(name, "super-secret-value")

	resp, err := http.Get(ts.URL + "/api/secrets?names=" + name + ",WEE_TEST_SECRET_UNSET")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var status map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status[name] {
		t.Errorf("status[%s] = false, want true", name)
	}
	if status["WEE_TEST_SECRET_UNSET"] {
		t.Error(`status["WEE_TEST_SECRET_UNSET"] = true, want false`)
	}
}

func TestSetSecretAppliesToProcessEnvironmentImmediately(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	const name = "WEE_TEST_SET_SECRET"
	os.Unsetenv(name)
	t.Cleanup(func() { os.Unsetenv(name) })

	body, _ := json.Marshal(setSecretRequest{Name: name, Value: "sk-live-example"})
	resp, err := http.Post(ts.URL+"/api/secrets", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := os.Getenv(name); got != "sk-live-example" {
		t.Errorf("os.Getenv(%s) = %q, want sk-live-example", name, got)
	}
}

func TestSetSecretRequiresName(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(setSecretRequest{Value: "no name attached"})
	resp, err := http.Post(ts.URL+"/api/secrets", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnsetSecretClearsIt(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	const name = "WEE_TEST_UNSET_ME_SECRET"
	os.Setenv(name, "will be cleared")
	t.Cleanup(func() { os.Unsetenv(name) })

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/secrets?name="+name, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if _, ok := os.LookupEnv(name); ok {
		t.Error("env var still set after DELETE /api/secrets")
	}
}

// TestCrossOriginRequestsRefused is the regression for the control plane's
// former wildcard CORS: a page on any other origin could PUT settings (e.g. a
// provider base URL pointing at itself, then POST /api/run to receive the API
// key), set env vars, or approve mutations. A non-loopback Origin is refused
// before any handler runs, and nothing it sent is applied.
func TestCrossOriginRequestsRefused(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, origin := range []string{"https://evil.example", "null", "http://192.168.1.10:5173"} {
		pre, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/settings", nil)
		pre.Header.Set("Origin", origin)
		pre.Header.Set("Access-Control-Request-Method", http.MethodPut)
		resp, err := http.DefaultClient.Do(pre)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight from %s: status %d, allow-origin %q; want 403 and no CORS grant", origin, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}

		put, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{"providerBaseUrls":{"openai":"https://evil.example/v1"}}`))
		put.Header.Set("Origin", origin)
		put.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(put)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("PUT /api/settings from %s: status %d, want 403", origin, resp.StatusCode)
		}
	}

	resp, err := http.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "evil.example") {
		t.Errorf("a refused cross-origin PUT was still applied: %s", body)
	}
}

// TestSameOriginAndNoOriginAllowed: the --ui-dir build (same origin) and
// non-browser clients (no Origin header) are unaffected by the origin policy.
func TestSameOriginAndNoOriginAllowed(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, origin := range []string{ts.URL, ""} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/settings", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /api/settings with Origin %q: status %d, want 200", origin, resp.StatusCode)
		}
	}
}

// TestSetSecretRejectsNonCredentialNames: /api/secrets may only set names that
// look like credentials, never process-control variables every tool subprocess
// inherits.
func TestSetSecretRejectsNonCredentialNames(t *testing.T) {
	s, _ := fastServer(t, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, name := range []string{"PATH", "GOFLAGS", "GIT_CONFIG_COUNT", "HTTPS_PROXY", "WEE_WORKSPACE_ROOT", "openai_api_key"} {
		before, had := os.LookupEnv(name)
		body, _ := json.Marshal(setSecretRequest{Name: name, Value: "x"})
		resp, err := http.Post(ts.URL+"/api/secrets", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST /api/secrets name=%s: status %d, want 400", name, resp.StatusCode)
		}
		if after, has := os.LookupEnv(name); has != had || after != before {
			t.Errorf("%s was modified by a rejected request", name)
		}
	}
}

func TestIsLoopbackHostAndAddr(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "[::1]": true, "evil.example": false, "0.0.0.0": false, "192.168.1.10": false} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
	for addr, want := range map[string]bool{"127.0.0.1:7676": true, "localhost:7676": true, "0.0.0.0:7676": false, ":7676": false} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
	h := withLoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for host, want := range map[string]int{"127.0.0.1:7676": http.StatusOK, "localhost:7676": http.StatusOK, "rebound.evil.example:7676": http.StatusMisdirectedRequest} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: status %d, want %d", host, rec.Code, want)
		}
	}
}

// TestSummarizeCountsFailureSpend: an execution's spend includes the billed
// attempts a Failure reports (REQ-BUDGET-03), not only WorkerFinished totals.
func TestSummarizeCountsFailureSpend(t *testing.T) {
	sum := summarize("x", []domain.Event{
		{Type: domain.WorkerFinished, NodeID: "a", Payload: map[string]any{"costUsd": 0.01, "tokens": float64(5)}},
		{Type: domain.Failure, NodeID: "b", Payload: map[string]any{"error": "contract violation", "costUsd": 0.03, "tokens": float64(30)}},
	})
	if sum.SpentTokens != 35 || sum.SpentCostUSD < 0.0399 || sum.SpentCostUSD > 0.0401 {
		t.Errorf("summary spend = $%v / %d tok, want $0.04 / 35 tok", sum.SpentCostUSD, sum.SpentTokens)
	}
}
