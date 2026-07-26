// Package replay renders and re-runs past executions (REQ-REPLAY-01..03). It
// is a thin layer over core/eventlog, core/store, and core/engine: Audit reads
// what an execution already wrote, at zero cost and zero model/tool calls;
// Reexecute runs the frozen snapshot again through the same Scheduler so the
// node cache (M1.6) naturally reuses unchanged nodes; Divergence compares two
// Timelines node by node. See docs/replay-honesty.md for what these two modes
// do and do not guarantee.
package replay

import (
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// NodeRecord is one node's outcome as reconstructed purely from the event
// log — the same facts a live engine.Result reports (state, artifact
// identity, cost), plus the artifact's actual bytes, so the record is
// self-sufficient. State is one of engine's NodeState constants; a node with
// no recorded event is StateSkipped if the execution reached
// ExecutionFinished (it was never on the path taken), or StatePending
// otherwise (the run stopped before reaching it — crash or still in flight).
type NodeRecord struct {
	State   engine.NodeState    `json:"state"`
	Hash    string              `json:"hash,omitempty"`
	Type    domain.ArtifactType `json:"type,omitempty"`
	Content []byte              `json:"content,omitempty"` // base64 on the wire (encoding/json default for []byte)
	CostUSD float64             `json:"costUsd,omitempty"`
	Tokens  int64               `json:"tokens,omitempty"`
	Err     string              `json:"error,omitempty"`
}

// Timeline is a fully reconstructed execution: the frozen workflow it ran,
// every event in write order, and each node's final outcome. Nothing in a
// Timeline is re-run — it is exactly what Audit read from disk.
type Timeline struct {
	ExecutionID  string                `json:"executionId"`
	Workflow     domain.Workflow       `json:"workflow"`
	Budget       domain.Budget         `json:"budget"`
	Events       []domain.Event        `json:"events"`
	Nodes        map[string]NodeRecord `json:"nodes"`
	SpentCostUSD float64               `json:"spentCostUsd"`
	SpentTokens  int64                 `json:"spentTokens"`
	// DefinitionHashes are the content hashes of the definitions this execution
	// pinned at start (worker "id@version" → hash), read straight from the frozen
	// snapshot (REQ-VERSION-02). Audit never consults a registry — this is the
	// pinned record, immune to any registry change made since the run. nil if the
	// run pinned nothing (not registry-driven).
	DefinitionHashes map[string]string `json:"definitionHashes,omitempty"`
	// Workers is the full resolved Worker definition (goal, contract,
	// contextPolicy) behind each DefinitionHashes entry, pinned the same way
	// (REQ-UI-03) — the Inspector's source for a node's Contract.
	Workers map[string]domain.Worker `json:"workers,omitempty"`
	// Inputs is the resolved value (supplied or Default) behind every declared
	// Workflow.Inputs entry this execution actually used (REQ-INPUT-01) — what
	// this run was run against. Not a secret, so unlike a "${env:...}" value it
	// belongs here in the audit record.
	Inputs map[string]string `json:"inputs,omitempty"`
	// ConnectionRefs is the non-secret provider/source resolution frozen for
	// this execution (REQ-CONN-06). The UI uses it with Workers to explain the
	// provider/model that actually ran rather than a later authored value
	// (REQ-UI-19).
	ConnectionRefs map[string]engine.ConnectionRef `json:"connectionRefs,omitempty"`
}

// Auditor renders past executions from their on-disk record alone (REQ-
// REPLAY-01): zero model calls, zero cost. It never constructs or touches a
// Scheduler or a NodeExecutor — Audit cannot reach a model or a tool even by
// accident, because it holds no reference to either.
type Auditor struct {
	log   *eventlog.Log
	store *store.Store
}

// NewAuditor builds an Auditor over the same event log and artifact store an
// engine.Scheduler wrote its executions to.
func NewAuditor(log *eventlog.Log, st *store.Store) *Auditor {
	return &Auditor{log: log, store: st}
}

// Audit reconstructs executionID's Timeline from its snapshot.json and
// events.jsonl (and the artifact store for each node's recorded content) —
// no network, no model, no tool invocation.
func (a *Auditor) Audit(executionID string) (Timeline, error) {
	var snap engine.Snapshot
	if err := a.log.ReadSnapshot(executionID, &snap); err != nil {
		return Timeline{}, fmt.Errorf("replay: audit %s: %w", executionID, err)
	}
	events, err := a.log.ReadAll(executionID)
	if err != nil {
		return Timeline{}, fmt.Errorf("replay: audit %s: %w", executionID, err)
	}

	nodes := make(map[string]NodeRecord, len(snap.Workflow.Nodes))
	finished := false
	for _, ev := range events {
		if ev.Type == domain.ExecutionFinished {
			finished = true
		}
		if ev.NodeID == "" {
			continue
		}
		rec := nodes[ev.NodeID]
		switch ev.Type {
		case domain.ArtifactCreated:
			if h, ok := ev.Payload["hash"].(string); ok {
				rec.Hash = h
			}
			if t, ok := ev.Payload["type"].(string); ok {
				rec.Type = domain.ArtifactType(t)
			}
		case domain.WorkerFinished:
			rec.State = engine.StateSucceeded
			if c, ok := ev.Payload["costUsd"].(float64); ok {
				rec.CostUSD = c
			}
			if t, ok := ev.Payload["tokens"].(float64); ok {
				rec.Tokens = int64(t)
			}
		case domain.Failure:
			rec.State = engine.StateFailed
			if e, ok := ev.Payload["error"].(string); ok {
				rec.Err = e
			}
		}
		nodes[ev.NodeID] = rec
	}

	var totalCost float64
	var totalTokens int64
	for _, n := range snap.Workflow.Nodes {
		rec, ok := nodes[n.ID]
		if !ok {
			state := engine.StatePending
			if finished {
				state = engine.StateSkipped
			}
			nodes[n.ID] = NodeRecord{State: state}
			continue
		}
		if rec.Hash != "" {
			content, err := a.store.Get(rec.Hash)
			if err != nil {
				return Timeline{}, fmt.Errorf("replay: audit %s: load artifact for node %q: %w", executionID, n.ID, err)
			}
			rec.Content = content
			nodes[n.ID] = rec
		}
		totalCost += rec.CostUSD
		totalTokens += rec.Tokens
	}

	return Timeline{
		ExecutionID:      executionID,
		Workflow:         snap.Workflow,
		Budget:           snap.Budget,
		Events:           events,
		Nodes:            nodes,
		SpentCostUSD:     totalCost,
		SpentTokens:      totalTokens,
		DefinitionHashes: snap.DefinitionHashes,
		Workers:          snap.Workers,
		Inputs:           snap.Inputs,
		ConnectionRefs:   snap.ConnectionRefs,
	}, nil
}
