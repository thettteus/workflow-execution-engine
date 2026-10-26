// Package server is the local control plane: it exposes a recorded or in-flight
// execution's event stream over HTTP (so the UI can watch a run live, M1.12) and
// drives the run lifecycle — start, cancel, resume, retry-from-node, re-execute,
// clear cache, export bundle — plus durable, non-secret settings (M2.2, ADR
// 0012). History always remains the event log, the single source of truth
// (PRIN-02); the only process-local state the server holds is the registry of
// in-flight runs' cancel handles, reconciled against disk on startup (Reconcile)
// so a restart never loses or misreports a run.
//
// The live transport is WebSocket via github.com/coder/websocket (ADR 0010,
// superseding ADR 0009's Server-Sent Events choice): one JSON text frame per
// domain.Event — byte-identical to the line-delimited JSON `wee run --json`
// emits. The client consumes it with the browser's built-in WebSocket. The
// stream is still functionally one-directional (server pushes, client only
// listens) — WebSocket was chosen to match spec/ui.md REQ-UI-02's original
// wording literally, not because this milestone needs full duplex.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/core/settings"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/validate"
)

// Assembly is a runnable engine for one workflow: the wired Scheduler plus the
// resolved Workflow and the definition/Worker pins a fresh run records in its
// snapshot. The server drives it — owning the goroutine and cancel context —
// rather than the assembler launching runs itself, so every run started through
// the API is cancellable and reconcilable.
type Assembly struct {
	Scheduler        *engine.Scheduler
	Workflow         *domain.Workflow
	DefinitionHashes map[string]string
	Workers          map[string]domain.Worker
	ConnectionRefs   map[string]engine.ConnectionRef
}

// Assembler builds an Assembly from a workflow ref (a path under Dir). The CLI
// wires a runner-backed implementation (loading the workflow, its Workers, and
// its sandboxed tools); it returns an error for a missing or invalid workflow. A
// nil Assembler makes the server read-only: it still streams and audits existing
// executions, but every run control returns 501.
type Assembler func(ref string) (*Assembly, error)

// defaultPoll is how often the live WebSocket handler re-reads the log for new
// events. It matches `wee run`'s streamer tick: fast enough to feel live, cheap
// enough for a local dev tool. The client sees pushed frames, never this tail.
const defaultPoll = 40 * time.Millisecond

// Server is the local control plane over the workspace. Beyond the read side
// (list/audit/stream) it owns the run lifecycle: `runs` holds the cancel handle
// of each execution started in THIS process (the one bit of process-local
// state, reconciled against disk on startup), `settings` is the durable
// non-secret config, and `assemble`/`newID` are the injected seam that turns a
// workflow ref into a runnable engine without core/server importing the CLI.
type Server struct {
	log          *eventlog.Log
	store        *store.Store
	cache        *cache.Cache
	settings     *settings.Store
	validator    *validate.Validator
	workspace    string
	dir          string
	templatesDir string
	uiDir        string
	assemble     Assembler
	newID        func(workflowID string) string
	defaultCache engine.CacheMode
	runs         *runRegistry
	mux          *http.ServeMux
	poll         time.Duration
}

// Config configures a Server. Workspace is the only field most read-only callers
// need. Assemble + NewID enable the run controls (nil Assemble → a read-only
// server: streaming and audit work, every control returns 501). Dir resolves a
// run's workflow ref and a template import's unpacked files; TemplatesDir ""
// disables the gallery; DefaultCache is the fallback cache mode for started runs
// when neither the request nor persisted settings specify one.
type Config struct {
	// Workspace is the state directory the engine writes under
	// (conventionally ".workflow") — executions, artifacts, cache, settings.
	Workspace string
	// Assemble turns a workflow ref into a runnable engine; nil disables all run
	// controls (501). See Assembler.
	Assemble Assembler
	// NewID mints an execution id for a fresh run; required when Assemble is set.
	NewID func(workflowID string) string
	// DefaultCache is the fallback cache mode for started runs. "" → CacheOn.
	DefaultCache engine.CacheMode
	// Dir is the base directory a run's workflow ref — and a template import's
	// unpacked files — resolve against.
	Dir string
	// TemplatesDir holds `wee export` bundles (*.tar) for GET /api/templates
	// and POST /api/templates/{name}/import; "" means no templates configured.
	TemplatesDir string
	// UIDir, when set, serves a built UI from this directory at /. API routes
	// remain under /api and /healthz; unknown UI paths fall back to index.html.
	UIDir string
}

// New builds a Server per cfg. Config.Assemble may be nil (a read-only server
// that still streams and audits existing executions). Call Reconcile once before
// serving to settle runs a prior process left in flight.
func New(cfg Config) *Server {
	dir := cfg.Dir
	if dir == "" {
		dir = "."
	}
	validator, err := validate.NewValidator()
	if err != nil {
		// The embedded, CI-checked schemas.FS failing to compile is a
		// build-time defect (examples_test.go already compiles the same
		// schemas on every test run) — not a request-time error to recover
		// from, so this mirrors stdlib's regexp.MustCompile convention.
		panic(fmt.Sprintf("core/server: embedded schemas failed to compile: %v", err))
	}
	s := &Server{
		log:          eventlog.New(cfg.Workspace),
		store:        store.New(cfg.Workspace),
		cache:        cache.New(cfg.Workspace),
		settings:     settings.New(cfg.Workspace),
		validator:    validator,
		workspace:    cfg.Workspace,
		dir:          dir,
		templatesDir: cfg.TemplatesDir,
		uiDir:        cfg.UIDir,
		assemble:     cfg.Assemble,
		newID:        cfg.NewID,
		defaultCache: cfg.DefaultCache,
		runs:         newRunRegistry(),
		mux:          http.NewServeMux(),
		poll:         defaultPoll,
	}
	// Go 1.22 method+wildcard routing — no router dependency.
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/executions", s.handleList)
	s.mux.HandleFunc("GET /api/executions/{id}", s.handleAudit)
	s.mux.HandleFunc("GET /api/executions/{id}/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/executions/{id}/progress", s.handleProgress)
	s.mux.HandleFunc("GET /api/executions/{id}/bundle", s.handleBundle)
	s.mux.HandleFunc("POST /api/run", s.handleRun)
	s.mux.HandleFunc("POST /api/executions/{id}/cancel", s.handleCancel)
	s.mux.HandleFunc("POST /api/executions/{id}/resume", s.handleRetry)
	s.mux.HandleFunc("POST /api/executions/{id}/retry", s.handleRetry)
	s.mux.HandleFunc("POST /api/executions/{id}/reexecute", s.handleReexecute)
	s.mux.HandleFunc("GET /api/executions/{id}/approvals", s.handleListApprovals)
	s.mux.HandleFunc("POST /api/executions/{id}/approvals/{checkpoint}/approve", s.handleApprove)
	s.mux.HandleFunc("POST /api/executions/{id}/approvals/{checkpoint}/reject", s.handleReject)
	s.mux.HandleFunc("POST /api/cache/clear", s.handleCacheClear)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	s.mux.HandleFunc("GET /api/templates", s.handleListTemplates)
	s.mux.HandleFunc("POST /api/templates/{name}/import", s.handleImportTemplate)
	s.mux.HandleFunc("GET /api/workers/{id}", s.handleListWorkerVersions)
	s.mux.HandleFunc("POST /api/workers", s.handleSaveWorker)
	s.mux.HandleFunc("GET /api/secrets", s.handleSecretsStatus)
	s.mux.HandleFunc("POST /api/secrets", s.handleSetSecret)
	s.mux.HandleFunc("DELETE /api/secrets", s.handleUnsetSecret)
	if cfg.UIDir != "" {
		s.mux.HandleFunc("GET /", s.handleUI)
	}
	return s
}

// Handler returns the HTTP handler, wrapped in the origin policy (withCORS) that
// lets the local UI — the Vite dev server on another loopback port, or the
// --ui-dir build on this same origin — call it, and refuses every other origin.
func (s *Server) Handler() http.Handler { return withCORS(s.mux) }

// ListenAndServe runs the server until the process exits. No write timeout is
// set: the live WebSocket connection is deliberately long-lived.
func (s *Server) ListenAndServe(addr string) error {
	h := s.Handler()
	if isLoopbackAddr(addr) {
		h = withLoopbackHost(h)
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.uiDir == "" {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	path := filepath.Join(s.uiDir, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		path = filepath.Join(s.uiDir, "index.html")
	}
	http.ServeFile(w, r, path)
}

// ExecutionSummary is one row of GET /api/executions. Workflow/Version come from
// the ExecutionStarted event; State is "running" until an ExecutionFinished
// event carries the terminal state (PRIN-02: derived from the log, not a
// separate status store). SpentCostUSD/SpentTokens/DurationMs are cheap to
// derive from the same event read as everything else here (M1.14's history
// table, REQ-METRIC-01/02) — unlike the Inspector's per-node metrics (M1.13's
// richer Audit), a list row never needs artifact bytes, so this stays a plain
// event-log summary, no core/store touch.
type ExecutionSummary struct {
	ID           string  `json:"id"`
	Workflow     string  `json:"workflow"`
	Version      string  `json:"version"`
	State        string  `json:"state"`
	SpentCostUSD float64 `json:"spentCostUsd"`
	SpentTokens  int64   `json:"spentTokens"`
	// DurationMs is 0 until ExecutionFinished (an in-flight run's duration is
	// the live client's own concern, ticked from ExecutionStarted — the same
	// pattern Timeline.tsx already uses for a running node's bar).
	DurationMs int64 `json:"durationMs"`
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	ids := s.listExecutionIDs()
	out := make([]ExecutionSummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.summarize(id))
	}
	writeJSON(w, http.StatusOK, out)
}

// Audit is GET /api/executions/{id}: a full reconstruction of the execution —
// the frozen workflow (so the Inspector can show each node's Contract via
// Workers), every recorded event (the same reducer the live stream feeds, one
// code path for live or replayed), and each node's outcome plus its artifact's
// actual bytes (base64 on the wire) — everything REQ-UI-03/REQ-UI-04 need,
// reusing core/replay's reconstruction rather than re-deriving it.
type Audit struct {
	replay.Timeline
	State string `json:"state"`
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tl, err := replay.NewAuditor(s.log, s.store).Audit(id)
	if err != nil {
		http.Error(w, "unknown execution", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, Audit{Timeline: tl, State: summarize(id, tl.Events).State})
}

// handleEvents upgrades to WebSocket and streams an execution's events,
// replaying everything recorded so far and then tailing live until the
// terminal ExecutionFinished event (always the last event a run emits —
// engine/scheduler.go), at which point the connection is closed with a normal
// closure status. A transient read error (log not yet created, or a torn
// trailing line under a concurrent append) is retried on the next tick, never
// surfaced — no event is lost or duplicated. OriginPatterns admits the same
// loopback UI origins withCORS does (same-origin is always allowed by the
// library): the origin check is WebSocket's own cross-origin gate — the CORS
// headers withCORS sets have no effect on the upgrade itself.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: loopbackOriginPatterns})
	if err != nil {
		return // Accept already wrote the appropriate HTTP error response
	}
	defer func() { _ = conn.CloseNow() }() // best-effort if we return before an explicit Close

	id := r.PathValue("id")
	ctx := r.Context()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()

	sent := 0
	for {
		if events, err := s.log.ReadAll(id); err == nil {
			for ; sent < len(events); sent++ {
				data, err := json.Marshal(events[sent])
				if err != nil {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					return // client disconnected mid-write
				}
				// Close on a terminal event only when the run is not (again) in
				// flight: a resumed/retried run reuses its id and appends a second
				// ExecutionFinished, so closing on the first would hide the new
				// work. While it is in this process's run registry, keep tailing.
				if events[sent].Type == domain.ExecutionFinished && !s.runs.running(id) {
					_ = conn.Close(websocket.StatusNormalClosure, "")
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runRequest is POST /api/run's body. Cache and BudgetUsd are the per-run
// overrides (REQ-CTRL-07); both optional — omitted falls back to persisted
// settings, then the server default / workflow budget.
type runRequest struct {
	Workflow                      string            `json:"workflow"`
	Inputs                        map[string]string `json:"inputs,omitempty"`
	Cache                         string            `json:"cache,omitempty"`
	BudgetUsd                     float64           `json:"budgetUsd,omitempty"`
	Connections                   []string          `json:"connections,omitempty"`
	AllowMutationsWithoutApproval bool              `json:"allowMutationsWithoutApproval,omitempty"`
}

type runResponse struct {
	ExecutionID string `json:"executionId"`
}

// handleRun starts a fresh execution (REQ-CTRL-03). The server owns the run: it
// mints the id, persists the run params so the run can later be resumed/retried/
// re-executed, launches the Scheduler on a cancellable background context (NOT
// the request's — that ends when the POST returns), and registers its cancel
// handle so cancel and restart-reconciliation can find it.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if s.assemble == nil || s.newID == nil {
		http.Error(w, "run not configured on this server", http.StatusNotImplemented)
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Workflow == "" {
		http.Error(w, "workflow is required", http.StatusBadRequest)
		return
	}
	asm, err := s.assemble(req.Workflow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cacheMode := s.effectiveCache(req.Cache)
	budget := s.effectiveBudget(asm.Workflow, req.BudgetUsd)
	connectionRefs, err := s.resolveConnectionRefs(asm, req.Connections)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	execID := s.newID(asm.Workflow.ID)
	if err := s.writeRunParams(execID, runParams{
		Workflow:                      req.Workflow,
		Inputs:                        req.Inputs,
		Cache:                         string(cacheMode),
		BudgetUSD:                     budget.MaxCostUSD,
		AllowMutationsWithoutApproval: req.AllowMutationsWithoutApproval,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	opts := engine.RunOptions{
		ExecutionID:              execID,
		Budget:                   budget,
		Cache:                    cacheMode,
		DefinitionHashes:         asm.DefinitionHashes,
		Workers:                  asm.Workers,
		Inputs:                   req.Inputs,
		ConnectionRefs:           connectionRefs,
		AllowUnattendedMutations: req.AllowMutationsWithoutApproval,
	}
	s.launch(execID, func(ctx context.Context) (*engine.Result, error) {
		return asm.Scheduler.Run(ctx, asm.Workflow, opts)
	})
	writeJSON(w, http.StatusOK, runResponse{ExecutionID: execID})
}

func (s *Server) resolveConnectionRefs(asm *Assembly, requested []string) (map[string]engine.ConnectionRef, error) {
	out := map[string]engine.ConnectionRef{}
	for id, ref := range asm.ConnectionRefs {
		out[id] = ref
	}
	if len(requested) == 0 {
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	set, err := s.settings.Load()
	if err != nil {
		return nil, err
	}
	byID := map[string]settings.Connection{}
	for _, c := range set.Connections {
		byID[c.ID] = c
	}
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown connection %q", id)
		}
		out[id] = connectionRef(c)
	}
	return out, nil
}

func connectionRef(c settings.Connection) engine.ConnectionRef {
	return engine.ConnectionRef{
		ID:        c.ID,
		Label:     c.Label,
		Kind:      string(c.Kind),
		Type:      c.Type,
		BaseURL:   c.BaseURL,
		SecretEnv: c.SecretEnv,
		Defaults:  c.Defaults,
	}
}

// Template is one row of GET /api/templates — enough for the gallery card
// (M1.14, REQ-UI-05; cost/tools/write-capable/inputs added M2.3, REQ-UI-16's
// "declare whether they can write before a user starts them"). The bundle
// itself is only decoded (registry.Import), never registered against
// anything persistent — listing is read-only.
type Template struct {
	Name                string   `json:"name"`
	WorkflowID          string   `json:"workflowId"`
	Version             string   `json:"version"`
	NodeCount           int      `json:"nodeCount"`
	RequiredConnections []string `json:"requiredConnections"`
	registry.TemplateFacts
}

func (s *Server) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	out := []Template{}
	if s.templatesDir == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	entries, err := os.ReadDir(s.templatesDir)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.templatesDir, e.Name()))
		if err != nil {
			continue // a template that fails to read just doesn't appear — no partial gallery crash
		}
		reg, err := registry.Import(data)
		if err != nil {
			continue
		}
		_, wf, ok := reg.SoleWorkflow()
		if !ok {
			continue
		}
		out = append(out, Template{
			Name:                strings.TrimSuffix(e.Name(), ".tar"),
			WorkflowID:          wf.ID,
			Version:             wf.Version,
			NodeCount:           len(wf.Nodes),
			RequiredConnections: requiredModelConnections(reg.Workers(wf)),
			TemplateFacts:       registry.DeriveTemplateFacts(wf),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func requiredModelConnections(workers map[string]domain.Worker) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, worker := range workers {
		provider := worker.Model.Provider
		if provider == "" {
			provider = "openai"
		}
		if !seen[provider] {
			seen[provider] = true
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}

type importTemplateResponse struct {
	// WorkflowPath is what the UI passes back as POST /api/run's "workflow"
	// field — a relative path (with a subdirectory, unlike a browser file
	// input's bare basename) that resolves against this server's own Dir,
	// since the files were just written there.
	WorkflowPath string          `json:"workflowPath"`
	Workflow     domain.Workflow `json:"workflow"`
}

// handleImportTemplate unpacks a template bundle (registry.Import) and writes
// its workflow + Workers as real YAML files under the workspace state dir, not
// the user's workflow root. This keeps "try this template" from littering the
// repository root with pr-review/, change-risk/, etc. while preserving the same
// file-resolution path POST /api/run already uses: the response returns a path
// relative to Dir, so runner.Load still reads ordinary YAML from disk.
func (s *Server) handleImportTemplate(w http.ResponseWriter, r *http.Request) {
	if s.templatesDir == "" {
		http.Error(w, "templates not configured on this server", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("name")
	data, err := os.ReadFile(filepath.Join(s.templatesDir, name+".tar"))
	if err != nil {
		http.Error(w, "unknown template", http.StatusNotFound)
		return
	}
	reg, err := registry.Import(data)
	if err != nil {
		http.Error(w, "corrupt template bundle: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, wf, ok := reg.SoleWorkflow()
	if !ok {
		http.Error(w, "template bundle does not contain exactly one workflow", http.StatusInternalServerError)
		return
	}
	workers := reg.Workers(wf)

	destDir := s.importedTemplateDir(name)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, "create destination directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	wfYAML, err := serialize.MarshalYAML(wf)
	if err != nil {
		http.Error(w, "marshal workflow: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(filepath.Join(destDir, "workflow.yaml"), wfYAML, 0o644); err != nil {
		http.Error(w, "write workflow.yaml: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for ref, worker := range workers {
		data, err := serialize.MarshalYAML(worker)
		if err != nil {
			http.Error(w, "marshal worker "+ref+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		id, _, _ := strings.Cut(ref, "@")
		if err := os.WriteFile(filepath.Join(destDir, id+".worker.yaml"), data, 0o644); err != nil {
			http.Error(w, "write "+id+".worker.yaml: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.copyTemplateConfig(name, destDir); err != nil {
		http.Error(w, "write wee.yaml: "+err.Error(), http.StatusInternalServerError)
		return
	}

	workflowPath, err := filepath.Rel(s.dir, filepath.Join(destDir, "workflow.yaml"))
	if err != nil {
		http.Error(w, "resolve imported workflow path: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, importTemplateResponse{
		WorkflowPath: workflowPath,
		Workflow:     wf,
	})
}

func (s *Server) importedTemplateDir(name string) string {
	return filepath.Join(s.workspace, "imported-templates", name)
}

// copyTemplateConfig carries a template's sidecar wee.yaml into the imported
// workflow directory when the examples tree provides one. The registry bundle
// itself intentionally contains only versioned definitions, but a runnable
// template also needs its deny-first tool allowlists (for example
// pr-review-autofix's api.github.com HTTP allowlist).
func (s *Server) copyTemplateConfig(name, destDir string) error {
	src := filepath.Join(filepath.Dir(s.templatesDir), name, "wee.yaml")
	data, err := os.ReadFile(src)
	switch {
	case err == nil:
		return os.WriteFile(filepath.Join(destDir, "wee.yaml"), data, 0o644)
	case os.IsNotExist(err):
		return nil
	default:
		return err
	}
}

// workerVersionsResponse is GET /api/workers/{id}'s body — every version of
// that Worker id found on disk, oldest first, so the UI's version-history
// picker and "current" editable copy (the last entry) both come from one
// call. dir (a query param, "" for the server's own --dir root) lets the
// caller scope the scan to wherever the currently-open workflow's sibling
// Worker files actually live, including hidden workspace-state imports.
type workerVersionsResponse struct {
	Versions []domain.Worker `json:"versions"`
}

func (s *Server) handleListWorkerVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	scanDir := filepath.Join(s.dir, r.URL.Query().Get("dir"))
	versions, err := scanWorkerVersions(scanDir, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, workerVersionsResponse{Versions: versions})
}

type saveWorkerRequest struct {
	Worker domain.Worker `json:"worker"`
	Dir    string        `json:"dir"`
}

type saveWorkerResponse struct {
	Worker domain.Worker `json:"worker"`
}

// handleSaveWorker is M1.14c's in-UI editing write path: the submitted
// Worker's own Version field is never trusted as the version to write —
// editing always mints a new version (owner-confirmed 2026-07-20: "editar
// cria uma versão nova automaticamente, mas salva a anterior"), computed as
// one patch bump past whatever's already on disk for that id. The file the
// edit started from is never touched; LoadWorkers already resolves any
// *.worker.yaml file by its internal id/version fields, not by filename, so
// two versions of the same Worker coexisting as two files is the existing,
// unmodified loading behavior (cli/internal/runner/assemble.go) — nothing
// engine-side changes to make rollback possible.
func (s *Server) handleSaveWorker(w http.ResponseWriter, r *http.Request) {
	var req saveWorkerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Worker.ID == "" {
		http.Error(w, "worker.id is required", http.StatusBadRequest)
		return
	}
	// M2.3: reject an invalid Worker before it ever reaches disk. Two checks,
	// not one — worker.schema.json's envelope check only requires
	// outputSchema to be *some* object; it can't see whether that object is
	// itself a compilable JSON Schema, so CompileSchema catches what
	// Validate(KindWorker) structurally cannot.
	if err := s.validator.Validate(validate.KindWorker, req.Worker, nil); err != nil {
		http.Error(w, "worker failed schema validation: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := validate.CompileSchema(req.Worker.Contract.OutputSchema); err != nil {
		http.Error(w, "worker's output schema does not compile: "+err.Error(), http.StatusBadRequest)
		return
	}
	scanDir := filepath.Join(s.dir, req.Dir)
	existing, err := scanWorkerVersions(scanDir, req.Worker.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Worker.Version = nextPatchVersion(existing)

	data, err := serialize.MarshalYAML(req.Worker)
	if err != nil {
		http.Error(w, "marshal worker: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		http.Error(w, "create directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fileName := req.Worker.ID + "@" + req.Worker.Version + ".worker.yaml"
	if err := os.WriteFile(filepath.Join(scanDir, fileName), data, 0o644); err != nil {
		http.Error(w, "write "+fileName+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, saveWorkerResponse{Worker: req.Worker})
}

// handleSecretsStatus reports which of the requested env var names currently
// have a value set — never the value itself, only presence (M1.14e). The UI's
// Settings panel uses this to render "● set" / "○ not set" per field without
// a secret ever making a round trip back to the browser once written.
func (s *Server) handleSecretsStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]bool{}
	for _, name := range strings.Split(r.URL.Query().Get("names"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		_, status[name] = os.LookupEnv(name)
	}
	writeJSON(w, http.StatusOK, status)
}

// secretNameRE bounds which env vars /api/secrets may set or clear to names
// shaped like credentials. The variables are inherited by every tool subprocess
// (go, git, ...), so an unrestricted name would let a caller rewrite PATH,
// GOFLAGS, GIT_*, proxy settings, or WEE_WORKSPACE_ROOT — process control, not a
// secret.
var secretNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(KEY|TOKEN|AUTH_HEADER|SECRET|PASSWORD)$`)

func secretNameHint(name string) string {
	return fmt.Sprintf("%q is not a credential env var name: use UPPER_SNAKE_CASE ending in _KEY, _TOKEN, _AUTH_HEADER, _SECRET or _PASSWORD", name)
}

type setSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// handleSetSecret applies name=value to this process's own environment,
// in-memory only (M1.14e, owner-confirmed 2026-07-20: never written to disk).
// The engine already reads "${env:NAME}" placeholders and each model
// provider's API key fresh at call time — os.Setenv here is the entire
// mechanism; no engine or provider-registry change is needed for the next run
// to pick up the new value.
func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	var req setSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if !secretNameRE.MatchString(req.Name) {
		http.Error(w, secretNameHint(req.Name), http.StatusBadRequest)
		return
	}
	if err := os.Setenv(req.Name, req.Value); err != nil {
		http.Error(w, "set "+req.Name+": "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnsetSecret clears a previously set env var (the Settings panel's
// "clear" action).
func (s *Server) handleUnsetSecret(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if !secretNameRE.MatchString(name) {
		http.Error(w, secretNameHint(name), http.StatusBadRequest)
		return
	}
	_ = os.Unsetenv(name)
	w.WriteHeader(http.StatusNoContent)
}

// scanWorkerVersions reads every *.worker.yaml/*.worker.yml file directly in
// dir and returns the ones matching id, sorted oldest-version-first. A
// missing directory is an empty result, not an error (a workflow with no
// Workers, or one whose dir hasn't been created yet).
func scanWorkerVersions(dir, id string) ([]domain.Worker, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var out []domain.Worker
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".worker.yaml") && !strings.HasSuffix(name, ".worker.yml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var wk domain.Worker
		if err := serialize.UnmarshalYAML(data, &wk); err != nil {
			continue
		}
		if wk.ID == id {
			out = append(out, wk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return semverLess(out[i].Version, out[j].Version) })
	return out, nil
}

// nextPatchVersion returns one patch bump past the highest version in
// existing, or "1.0.0" if there are none yet. Only MAJOR.MINOR.PATCH is
// understood (every version in this project is plain semver) — a version
// string that doesn't parse as three integers is treated as lower than any
// that does, so a save still succeeds with a sane starting point rather than
// erroring on an unexpected format.
func nextPatchVersion(existing []domain.Worker) string {
	if len(existing) == 0 {
		return "1.0.0"
	}
	latest := existing[len(existing)-1].Version // scanWorkerVersions returns oldest-first
	major, minor, patch, ok := parseSemver(latest)
	if !ok {
		return "1.0.0"
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch+1)
}

func parseSemver(v string) (major, minor, patch int, ok bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if patch, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

func semverLess(a, b string) bool {
	aMaj, aMin, aPatch, aOK := parseSemver(a)
	bMaj, bMin, bPatch, bOK := parseSemver(b)
	if !aOK || !bOK {
		return a < b // unparseable — stable, arbitrary fallback, never crashes
	}
	if aMaj != bMaj {
		return aMaj < bMaj
	}
	if aMin != bMin {
		return aMin < bMin
	}
	return aPatch < bPatch
}

// listExecutionIDs returns recorded execution ids, newest first — ids are
// timestamped, so reverse-lexical is reverse-chronological.
func (s *Server) listExecutionIDs() []string {
	entries, err := os.ReadDir(filepath.Join(s.workspace, "executions"))
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

func (s *Server) summarize(id string) ExecutionSummary {
	events, err := s.log.ReadAll(id)
	if err != nil {
		return ExecutionSummary{ID: id}
	}
	return summarize(id, events)
}

// summarize derives a run's summary from its events alone. State defaults to
// "running" and is overwritten only by a terminal ExecutionFinished.
func summarize(id string, events []domain.Event) ExecutionSummary {
	sum := ExecutionSummary{ID: id, State: "running"}
	var startedAt, finishedAt time.Time
	for _, ev := range events {
		switch ev.Type {
		case domain.ExecutionStarted:
			sum.Workflow, _ = ev.Payload["workflow"].(string)
			sum.Version, _ = ev.Payload["version"].(string)
			startedAt = ev.Timestamp
		case domain.ExecutionFinished:
			if st, ok := ev.Payload["state"].(string); ok {
				sum.State = st
			}
			finishedAt = ev.Timestamp
		case domain.WorkerFinished:
			if c, ok := ev.Payload["costUsd"].(float64); ok {
				sum.SpentCostUSD += c
			}
			if t, ok := ev.Payload["tokens"].(float64); ok {
				sum.SpentTokens += int64(t)
			}
		}
	}
	if !startedAt.IsZero() && !finishedAt.IsZero() {
		sum.DurationMs = finishedAt.Sub(startedAt).Milliseconds()
	}
	return sum
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// loopbackOriginPatterns are the WebSocket origin patterns (matched against the
// Origin's host[:port]) for a UI served from a loopback address.
var loopbackOriginPatterns = []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*", `\[::1\]`, `\[::1\]:*`}

// withCORS is the control plane's origin policy. The API has no authentication
// yet (M2.7), so a browser origin is the only thing standing between a page the
// user happens to visit and endpoints that change settings, set env vars,
// approve mutations, or start runs. A request carrying an Origin is served only
// when that origin is this server's own (the --ui-dir build) or a loopback one
// (the Vite dev server); anything else is refused before it reaches a handler.
// A request with no Origin is not a browser cross-origin request (curl, the
// CLI) and passes. Allowed origins are echoed, never "*". Preflight OPTIONS is
// answered here so every route need not register it.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if !allowedOrigin(origin, r.Host) {
				http.Error(w, "cross-origin request refused: "+origin+" is not a local UI origin", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// allowedOrigin reports whether a browser Origin may call this server: its own
// origin (same host:port as the request), or any http(s) loopback origin.
func allowedOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	return isLoopbackHost(u.Hostname())
}

// withLoopbackHost rejects requests whose Host header is not a loopback name,
// for a server listening only on loopback. Without it, DNS rebinding (an
// attacker's domain re-resolved to 127.0.0.1) would make that domain's pages
// same-origin with this server and pass withCORS.
func withLoopbackHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if !isLoopbackHost(host) {
			http.Error(w, "refused: Host "+r.Host+" is not a loopback name", http.StatusMisdirectedRequest)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// isLoopbackAddr reports whether a listen address binds only loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
