package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/replay"
	"github.com/thettteus/workflow-execution-engine/core/settings"
)

// handleGetSettings returns the persisted, non-secret settings (REQ-CTRL-05).
// It never returns a secret value — the Settings type has no field that holds
// one (PRIN-10).
func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	set, err := s.settings.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// handlePutSettings persists settings. Any field a client sends that is not part
// of the Settings struct (e.g. an accidental raw key) is dropped on decode, so a
// secret value cannot be written to disk even if submitted (PRIN-10).
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var set settings.Settings
	if err := json.NewDecoder(r.Body).Decode(&set); err != nil {
		http.Error(w, "invalid settings body", http.StatusBadRequest)
		return
	}
	if err := s.settings.Save(set); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// Progress is a run's derived progress (REQ-CTRL-06): computed from the events
// already in the log plus this process's liveness, never persisted. Running is
// true only while THIS process is actively driving the run — the honest liveness
// signal distinguishing "working" from an interrupted run awaiting resume.
type Progress struct {
	State           string   `json:"state"`
	Running         bool     `json:"running"`
	TotalNodes      int      `json:"totalNodes"`
	CompletedNodes  int      `json:"completedNodes"`
	RunningNodes    []string `json:"runningNodes"`
	LastEventUnixMs int64    `json:"lastEventUnixMs"`
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := s.log.ReadAll(id)
	if err != nil {
		http.Error(w, "unknown execution", http.StatusNotFound)
		return
	}
	total := 0
	var snap engine.Snapshot
	if s.log.ReadSnapshot(id, &snap) == nil {
		total = len(snap.Workflow.Nodes)
	}
	p := deriveProgress(events, total)
	p.State = summarize(id, events).State
	p.Running = s.runs.running(id)
	writeJSON(w, http.StatusOK, p)
}

// deriveProgress folds an execution's events into node-completion counts and the
// last-event timestamp. State and Running are set by the caller (State from the
// same summarize the list uses; Running from the live registry). Pure over its
// inputs, so it is unit-tested directly.
func deriveProgress(events []domain.Event, totalNodes int) Progress {
	started := map[string]bool{}
	terminal := map[string]bool{}
	var last time.Time
	for _, ev := range events {
		if ev.Timestamp.After(last) {
			last = ev.Timestamp
		}
		if ev.NodeID == "" {
			continue
		}
		switch ev.Type {
		case domain.WorkerStarted:
			started[ev.NodeID] = true
		case domain.WorkerFinished, domain.Failure:
			// A cache hit reconstructs a WorkerFinished, so cached nodes count as
			// completed here too — exactly the "done" the UI wants to show.
			terminal[ev.NodeID] = true
		}
	}
	running := []string{}
	for id := range started {
		if !terminal[id] {
			running = append(running, id)
		}
	}
	p := Progress{
		TotalNodes:     totalNodes,
		CompletedNodes: len(terminal),
		RunningNodes:   running,
	}
	if !last.IsZero() {
		p.LastEventUnixMs = last.UnixMilli()
	}
	return p
}

// cacheClearRequest selects what to clear (REQ-CTRL-03): everything, a set of
// explicit keys, or the keys a given execution (optionally one node) recorded —
// the control plane resolves node/workflow granularity to keys from the recorded
// CacheHit/CacheMiss events.
type cacheClearRequest struct {
	All         bool     `json:"all,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	ExecutionID string   `json:"executionId,omitempty"`
	NodeID      string   `json:"nodeId,omitempty"`
}

type cacheClearResponse struct {
	Removed int `json:"removed"`
}

func (s *Server) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	var req cacheClearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.All {
		list, _ := s.cache.List()
		if err := s.cache.Clear(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, cacheClearResponse{Removed: len(list)})
		return
	}
	keys := req.Keys
	if req.ExecutionID != "" {
		keys = append(keys, s.cacheKeysForExecution(req.ExecutionID, req.NodeID)...)
	}
	if len(keys) == 0 {
		http.Error(w, "specify all, keys, or executionId", http.StatusBadRequest)
		return
	}
	removed, err := s.cache.Delete(keys...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cacheClearResponse{Removed: removed})
}

// cacheKeysForExecution collects the cache keys an execution recorded (from its
// CacheHit/CacheMiss events), optionally filtered to a single node — how the
// control plane turns "clear this node/workflow's cache" into concrete keys.
func (s *Server) cacheKeysForExecution(execID, nodeID string) []string {
	events, err := s.log.ReadAll(execID)
	if err != nil {
		return nil
	}
	var keys []string
	for _, ev := range events {
		if ev.Type != domain.CacheHit && ev.Type != domain.CacheMiss {
			continue
		}
		if nodeID != "" && ev.NodeID != nodeID {
			continue
		}
		if k, ok := ev.Payload["key"].(string); ok && k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// handleBundle streams an execution's portable bundle as a tar download
// (REQ-CTRL-03) — snapshot + events (verbatim) + referenced artifacts, no
// secrets (replay.ExportBundle).
func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, err := replay.ExportBundle(s.log, s.store, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "unknown execution", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".tar"))
	_, _ = w.Write(data)
}
