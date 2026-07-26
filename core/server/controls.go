package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/replay"
)

// runRegistry maps an in-flight execution id to the cancel func of the context
// its Scheduler runs under. It is the server's only mutable engine-adjacent
// state (ADR 0012): authoritative for *liveness* — "is this process running
// this execution right now" — never for history, which is always the log.
type runRegistry struct {
	mu   sync.Mutex
	runs map[string]context.CancelFunc
}

func newRunRegistry() *runRegistry { return &runRegistry{runs: make(map[string]context.CancelFunc)} }

func (r *runRegistry) add(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[id] = cancel
}

// cancel cancels the run's context if it is in flight, reporting whether it was.
func (r *runRegistry) cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.runs[id]
	if ok {
		c()
	}
	return ok
}

func (r *runRegistry) done(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, id)
}

func (r *runRegistry) running(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[id]
	return ok
}

// launch runs fn on a fresh background context (NOT a request context — the run
// must outlive the HTTP call), registering its cancel handle for the run's
// duration so cancel and reconciliation can find it. Errors surface through the
// event log the run writes, so fn's return is intentionally discarded.
func (s *Server) launch(execID string, fn func(context.Context) (*engine.Result, error)) {
	ctx, cancel := context.WithCancel(context.Background())
	s.runs.add(execID, cancel)
	go func() {
		defer s.runs.done(execID)
		defer cancel()
		_, _ = fn(ctx)
	}()
}

// Reconcile settles executions a prior process left in flight: any run with an
// ExecutionStarted but no terminal event (the process died mid-run) is closed
// with a Cancelled + terminal ExecutionFinished pair — existing catalog events,
// so REQ-EVENT-01 stays closed and the hash chain (ADR 0007) continues — leaving
// it resumable and never reported as silently running (REQ-CTRL-02). Call once
// before serving; safe to call when nothing needs reconciling.
func (s *Server) Reconcile() {
	for _, id := range s.listExecutionIDs() {
		events, err := s.log.ReadAll(id)
		if err != nil {
			continue
		}
		started, terminal := false, false
		for _, ev := range events {
			switch ev.Type {
			case domain.ExecutionStarted:
				started = true
			case domain.ExecutionFinished:
				terminal = true
			}
		}
		if !started || terminal {
			continue
		}
		now := time.Now().UTC()
		_ = s.log.Append(id, domain.Event{Type: domain.Cancelled, ExecutionID: id, Timestamp: now, Payload: map[string]any{"reason": "interrupted: wee serve restarted"}})
		_ = s.log.Append(id, domain.Event{Type: domain.ExecutionFinished, ExecutionID: id, Timestamp: now, Payload: map[string]any{"state": string(domain.ExecutionCancelled), "reason": "interrupted"}})
	}
}

// handleCancel cancels an in-flight run (REQ-CTRL-03). Cancellation flows through
// the engine's cooperative path (REQ-RUNTIME-05): it emits Cancelled, persists
// partial state, and finalizes. A run that is not in flight (already terminal)
// is a 409 — nothing to cancel.
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if s.runs.cancel(r.PathValue("id")) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	http.Error(w, "execution is not running", http.StatusConflict)
}

// retryRequest is the body of POST resume/retry. From "" resumes (re-runs failed
// and never-started nodes, reusing finished ones — this is retry-failed). A From
// node re-runs that node and its downstream too (retry-from-node, REQ-CTRL-03).
type retryRequest struct {
	From string `json:"from,omitempty"`
}

// handleRetry resumes or retries an execution (REQ-CTRL-03/04). It rebuilds the
// engine from the recorded run params, then runs Resume — or ResumeFrom when a
// node is named — so completed upstream work is reused, never repeated or
// re-charged. Refuses if the run is already in flight.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	if s.assemble == nil {
		http.Error(w, "run controls not configured on this server", http.StatusNotImplemented)
		return
	}
	id := r.PathValue("id")
	if s.runs.running(id) {
		http.Error(w, "execution is already running", http.StatusConflict)
		return
	}
	var req retryRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body is fine (plain resume)

	asm, _, err := s.assemblyFor(id)
	if err != nil {
		s.writeAssemblyError(w, err)
		return
	}
	from := strings.TrimSpace(req.From)
	s.launch(id, func(ctx context.Context) (*engine.Result, error) {
		if from != "" {
			return asm.Scheduler.ResumeFrom(ctx, id, from)
		}
		return asm.Scheduler.Resume(ctx, id)
	})
	writeJSON(w, http.StatusOK, runResponse{ExecutionID: id})
}

// handleReexecute re-runs a recorded execution's frozen workflow as a NEW
// execution (REQ-CTRL-03, REQ-REPLAY-02): the node cache reuses every unchanged
// node, only invalidated nodes reach a model or tool. The new run inherits the
// original's run params so it too can be resumed/retried later.
func (s *Server) handleReexecute(w http.ResponseWriter, r *http.Request) {
	if s.assemble == nil || s.newID == nil {
		http.Error(w, "run controls not configured on this server", http.StatusNotImplemented)
		return
	}
	id := r.PathValue("id")
	asm, rp, err := s.assemblyFor(id)
	if err != nil {
		s.writeAssemblyError(w, err)
		return
	}
	newID := s.newID(asm.Workflow.ID)
	if err := s.writeRunParams(newID, rp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rex := replay.NewReexecuter(s.log, asm.Scheduler)
	s.launch(newID, func(ctx context.Context) (*engine.Result, error) {
		return rex.Reexecute(ctx, id, newID)
	})
	writeJSON(w, http.StatusOK, runResponse{ExecutionID: newID})
}

// Approval is a pending or decided mutation checkpoint for one execution.
type Approval struct {
	CheckpointID string         `json:"checkpointId"`
	NodeID       string         `json:"nodeId"`
	Tool         string         `json:"tool"`
	Status       string         `json:"status"`
	Mutation     map[string]any `json:"mutation,omitempty"`
	Input        any            `json:"input,omitempty"`
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.approvals(r.PathValue("id"))
	if err != nil {
		http.Error(w, "unknown execution", http.StatusNotFound)
		return
	}
	out := make([]Approval, 0, len(approvals))
	for _, a := range approvals {
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	s.handleApprovalDecision(w, r, domain.ApprovalGranted)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	s.handleApprovalDecision(w, r, domain.ApprovalRejected)
}

func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request, decision domain.EventType) {
	if s.assemble == nil {
		http.Error(w, "run controls not configured on this server", http.StatusNotImplemented)
		return
	}
	id := r.PathValue("id")
	if s.runs.running(id) {
		http.Error(w, "execution is already running", http.StatusConflict)
		return
	}
	checkpoint := r.PathValue("checkpoint")
	approval, appended, err := s.appendApprovalDecision(id, checkpoint, decision)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "unknown or stale approval checkpoint", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	status := http.StatusAccepted
	if !appended {
		status = http.StatusOK
	} else {
		asm, _, err := s.assemblyFor(id)
		if err != nil {
			s.writeAssemblyError(w, err)
			return
		}
		s.launch(id, func(ctx context.Context) (*engine.Result, error) {
			return asm.Scheduler.Resume(ctx, id)
		})
	}
	writeJSON(w, status, approval)
}

func (s *Server) appendApprovalDecision(execID, checkpoint string, decision domain.EventType) (Approval, bool, error) {
	approvals, err := s.approvals(execID)
	if err != nil {
		return Approval{}, false, err
	}
	a, ok := approvals[checkpoint]
	if !ok {
		return Approval{}, false, os.ErrNotExist
	}
	switch a.Status {
	case "granted":
		if decision == domain.ApprovalGranted {
			return a, false, nil
		}
		return Approval{}, false, fmt.Errorf("approval checkpoint %s was already granted", checkpoint)
	case "rejected":
		if decision == domain.ApprovalRejected {
			return a, false, nil
		}
		return Approval{}, false, fmt.Errorf("approval checkpoint %s was already rejected", checkpoint)
	}
	status := "granted"
	if decision == domain.ApprovalRejected {
		status = "rejected"
	}
	payload := map[string]any{"checkpointId": checkpoint, "tool": a.Tool, "status": status}
	if err := s.log.Append(execID, domain.Event{Type: decision, ExecutionID: execID, NodeID: a.NodeID, Timestamp: time.Now().UTC(), Payload: payload}); err != nil {
		return Approval{}, false, err
	}
	a.Status = status
	return a, true, nil
}

func (s *Server) approvals(execID string) (map[string]Approval, error) {
	events, err := s.log.ReadAll(execID)
	if err != nil {
		return nil, err
	}
	out := map[string]Approval{}
	for _, ev := range events {
		switch ev.Type {
		case domain.ApprovalRequested:
			id := payloadString(ev.Payload, "checkpointId")
			if id == "" {
				continue
			}
			out[id] = Approval{
				CheckpointID: id,
				NodeID:       ev.NodeID,
				Tool:         payloadString(ev.Payload, "tool"),
				Status:       "pending",
				Mutation:     payloadMap(ev.Payload, "mutation"),
				Input:        ev.Payload["input"],
			}
		case domain.ApprovalGranted, domain.ApprovalRejected:
			id := payloadString(ev.Payload, "checkpointId")
			if id == "" {
				continue
			}
			a := out[id]
			a.CheckpointID = id
			a.NodeID = ev.NodeID
			a.Tool = payloadString(ev.Payload, "tool")
			if ev.Type == domain.ApprovalGranted {
				a.Status = "granted"
			} else {
				a.Status = "rejected"
			}
			out[id] = a
		}
	}
	return out, nil
}

func payloadString(payload map[string]any, key string) string {
	v, _ := payload[key].(string)
	return v
}

func payloadMap(payload map[string]any, key string) map[string]any {
	v, _ := payload[key].(map[string]any)
	return v
}

// assemblyFor rebuilds the engine for a recorded execution from its persisted
// run params (the workflow ref). Returns os.ErrNotExist (wrapped) if the
// execution or its run params are unknown.
func (s *Server) assemblyFor(execID string) (*Assembly, runParams, error) {
	rp, err := s.readRunParams(execID)
	if err != nil {
		return nil, runParams{}, err
	}
	asm, err := s.assemble(rp.Workflow)
	if err != nil {
		return nil, runParams{}, err
	}
	return asm, rp, nil
}

// writeAssemblyError maps an assemblyFor error to a status: a missing execution/
// run params is 404, anything else (a workflow that no longer loads/validates)
// is 400.
func (s *Server) writeAssemblyError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "unknown execution or missing run parameters (started before this server version?)", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// effectiveCache resolves the cache mode for a run: an explicit request value
// wins, then persisted settings, then the server default, else CacheOn.
func (s *Server) effectiveCache(reqCache string) engine.CacheMode {
	if m, ok := cacheModeFromString(reqCache); ok {
		return m
	}
	if set, err := s.settings.Load(); err == nil {
		if m, ok := cacheModeFromString(set.CacheMode); ok {
			return m
		}
	}
	if s.defaultCache != "" {
		return s.defaultCache
	}
	return engine.CacheOn
}

// effectiveBudget resolves the budget for a run: an explicit request override
// wins; otherwise the workflow's own budget, backfilled from the persisted
// default only when the workflow sets no cost cap of its own.
func (s *Server) effectiveBudget(wf *domain.Workflow, override float64) domain.Budget {
	b := wf.Budget
	switch {
	case override > 0:
		b.MaxCostUSD = override
	case b.MaxCostUSD == 0:
		if set, err := s.settings.Load(); err == nil && set.DefaultBudgetUSD > 0 {
			b.MaxCostUSD = set.DefaultBudgetUSD
		}
	}
	return b
}

func cacheModeFromString(s string) (engine.CacheMode, bool) {
	switch s {
	case "on":
		return engine.CacheOn, true
	case "off":
		return engine.CacheOff, true
	case "readonly":
		return engine.CacheReadOnly, true
	default:
		return "", false
	}
}

// runParams records how a run was started so it can be resumed, retried, or
// re-executed later without the caller re-supplying anything — the per-execution
// entry of M2.2's execution index (ADR 0012). It holds no secret: Inputs are
// non-secret by construction (REQ-INPUT-01), the workflow ref is a path, and
// secrets stay "${env:...}" references resolved fresh at call time.
type runParams struct {
	Workflow                      string            `json:"workflow"`
	Inputs                        map[string]string `json:"inputs,omitempty"`
	Cache                         string            `json:"cache,omitempty"`
	BudgetUSD                     float64           `json:"budgetUsd,omitempty"`
	AllowMutationsWithoutApproval bool              `json:"allowMutationsWithoutApproval,omitempty"`
}

const runParamsFile = "runparams.json"

func (s *Server) writeRunParams(execID string, rp runParams) error {
	dir := s.log.Dir(execID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("server: create execution dir: %w", err)
	}
	data, err := json.MarshalIndent(rp, "", "  ")
	if err != nil {
		return fmt.Errorf("server: encode run params: %w", err)
	}
	return atomicWrite(filepath.Join(dir, runParamsFile), data)
}

func (s *Server) readRunParams(execID string) (runParams, error) {
	data, err := os.ReadFile(filepath.Join(s.log.Dir(execID), runParamsFile))
	if err != nil {
		return runParams{}, err
	}
	var rp runParams
	if err := json.Unmarshal(data, &rp); err != nil {
		return runParams{}, fmt.Errorf("server: decode run params for %s: %w", execID, err)
	}
	return rp, nil
}

// atomicWrite writes data to path via a temp file in the same directory plus a
// rename, so a crash mid-write never leaves a truncated file (NFR-CTRL-01).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("server: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("server: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("server: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("server: commit: %w", err)
	}
	return nil
}
