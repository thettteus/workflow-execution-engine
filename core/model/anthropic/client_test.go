package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/model/anthropic"
)

// TestCompleteAgainstStub is REQ-MODEL-03: the hand-rolled client posts to
// /v1/messages with x-api-key + anthropic-version, hoists the system message,
// and reads usage.input_tokens/output_tokens.
func TestCompleteAgainstStub(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"ok\":true}"}],"usage":{"input_tokens":21,"output_tokens":5}}`)
	}))
	defer srv.Close()

	c := anthropic.New(anthropic.WithBaseURL(srv.URL+"/v1"), anthropic.WithAPIKey("ak-123"))
	resp, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleSystem, Content: "sys"}, {Role: model.RoleUser, Content: "u"}},
		model.Params{Model: "claude-3-5-haiku", Extra: map[string]any{"max_tokens": 256}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "ak-123" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion == "" {
		t.Errorf("anthropic-version header missing")
	}
	if gotBody["system"] != "sys" {
		t.Errorf("system not hoisted: %v", gotBody["system"])
	}
	// The system message must NOT appear in the turn list.
	turns, _ := gotBody["messages"].([]any)
	if len(turns) != 1 {
		t.Errorf("want 1 turn (user only), got %d: %v", len(turns), turns)
	}
	if mt, ok := gotBody["max_tokens"].(float64); !ok || mt != 256 {
		t.Errorf("max_tokens override not applied: %v", gotBody["max_tokens"])
	}
	if resp.Content != `{"ok":true}` || resp.InputTokens != 21 || resp.OutputTokens != 5 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantTrans bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusBadRequest, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":{"message":"x"}}`)
		}))
		c := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"))
		_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
		srv.Close()
		var te *model.TransientError
		var fe *model.FatalError
		if tc.wantTrans && !errors.As(err, &te) {
			t.Errorf("status %d: want TransientError, got %v", tc.status, err)
		}
		if !tc.wantTrans && !errors.As(err, &fe) {
			t.Errorf("status %d: want FatalError, got %v", tc.status, err)
		}
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	retryAt := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", retryAt)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	defer srv.Close()

	c := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	var te *model.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("want TransientError, got %T", err)
	}
	if !te.HasRetry || te.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %s, want positive HTTP-date delay", te.RetryAfter)
	}
}

func TestOversizedSuccessResponseIsRejectedAsPartialOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", 1<<20+2))
	}))
	defer srv.Close()

	c := anthropic.New(anthropic.WithBaseURL(srv.URL), anthropic.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	var fe *model.FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("want FatalError for oversized partial output, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "refusing partial output") {
		t.Fatalf("err = %v", err)
	}
}
