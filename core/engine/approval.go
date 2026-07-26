package engine

import (
	"errors"
	"fmt"

	"github.com/thettteus/workflow-execution-engine/core/canonical"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/tool"
)

var (
	ErrApprovalRequired = errors.New("engine: approval required")
	ErrApprovalRejected = errors.New("engine: approval rejected")
)

type approvalStatus string

const (
	approvalPending  approvalStatus = "pending"
	approvalGranted  approvalStatus = "granted"
	approvalRejected approvalStatus = "rejected"
)

type approvalRecord struct {
	CheckpointID string
	NodeID       string
	Tool         string
	Status       approvalStatus
	Mutation     tool.Mutation
}

type approvalRequiredError struct {
	CheckpointID string
	NodeID       string
	Tool         string
	Mutation     tool.Mutation
}

func (e approvalRequiredError) Error() string {
	return fmt.Sprintf("%v: node %q requires approval for %s", ErrApprovalRequired, e.NodeID, e.Mutation.Operation)
}

func (e approvalRequiredError) Unwrap() error { return ErrApprovalRequired }

type approvalRejectedError struct {
	CheckpointID string
	NodeID       string
	Tool         string
}

func (e approvalRejectedError) Error() string {
	return fmt.Sprintf("%v: node %q checkpoint %s was rejected", ErrApprovalRejected, e.NodeID, e.CheckpointID)
}

func (e approvalRejectedError) Unwrap() error { return ErrApprovalRejected }

func checkpointID(execID, nodeID, toolName string, mutation tool.Mutation, redactedInput []byte) (string, error) {
	return canonical.Hash(map[string]any{
		"executionId": execID,
		"nodeId":      nodeID,
		"tool":        toolName,
		"operation":   mutation.Operation,
		"input":       string(redactedInput),
	})
}

func (s *Scheduler) approvalRecords(execID string) map[string]approvalRecord {
	events, err := s.log.ReadAll(execID)
	if err != nil {
		return nil
	}
	out := map[string]approvalRecord{}
	for _, ev := range events {
		switch ev.Type {
		case domain.ApprovalRequested:
			rec := approvalRecord{
				CheckpointID: stringPayload(ev.Payload, "checkpointId"),
				NodeID:       ev.NodeID,
				Tool:         stringPayload(ev.Payload, "tool"),
				Status:       approvalPending,
			}
			if rec.CheckpointID != "" {
				out[rec.CheckpointID] = rec
			}
		case domain.ApprovalGranted, domain.ApprovalRejected:
			id := stringPayload(ev.Payload, "checkpointId")
			if id == "" {
				continue
			}
			rec := out[id]
			rec.CheckpointID = id
			rec.NodeID = ev.NodeID
			rec.Tool = stringPayload(ev.Payload, "tool")
			if ev.Type == domain.ApprovalGranted {
				rec.Status = approvalGranted
			} else {
				rec.Status = approvalRejected
			}
			out[id] = rec
		}
	}
	return out
}

func stringPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, _ := payload[key].(string)
	return v
}
