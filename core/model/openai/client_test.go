package openai_test

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
	"github.com/thettteus/workflow-execution-engine/core/model/openai"
)

// TestBaseURLOverrideTalksToStub is REQ-MODEL-04: the OpenAI client with an
// overridden base URL talks to a local stub server — proving any
// OpenAI-compatible endpoint (Ollama/vLLM) works with zero engine changes.
func TestBaseURLOverrideTalksToStub(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"score\":1}"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}))
	defer srv.Close()

	// Keyless local endpoint: no API key configured.
	c := openai.New(openai.WithBaseURL(srv.URL+"/v1"), openai.WithAPIKey(""))
	resp, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleSystem, Content: "be terse"}, {Role: model.RoleUser, Content: "hi"}},
		model.Params{Model: "llama3", Extra: map[string]any{"temperature": 0}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("keyless endpoint must not send Authorization, got %q", gotAuth)
	}
	if gotBody["model"] != "llama3" {
		t.Errorf("model not forwarded: %v", gotBody["model"])
	}
	if _, ok := gotBody["temperature"]; !ok {
		t.Errorf("Extra params not passed through: %v", gotBody)
	}
	if resp.Content != `{"score":1}` || resp.InputTokens != 11 || resp.OutputTokens != 7 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestSendsBearerWhenKeyed(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`)
	}))
	defer srv.Close()

	c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("sk-secret"))
	if _, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "gpt-4o"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Errorf("Authorization = %q, want Bearer sk-secret", gotAuth)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status      int
		retryAfter  string
		wantTrans   bool
		wantRetryFo bool
	}{
		{http.StatusTooManyRequests, "3", true, true},
		{http.StatusServiceUnavailable, "", true, false},
		{http.StatusInternalServerError, "", true, false},
		{http.StatusBadRequest, "", false, false},
		{http.StatusUnauthorized, "", false, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":"nope"}`)
		}))
		c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"))
		_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", tc.status)
		}
		var te *model.TransientError
		var fe *model.FatalError
		if tc.wantTrans {
			if !errors.As(err, &te) {
				t.Errorf("status %d: want TransientError, got %T", tc.status, err)
				continue
			}
			if tc.wantRetryFo && (!te.HasRetry || te.RetryAfter.Seconds() != 3) {
				t.Errorf("status %d: Retry-After not honored: %+v", tc.status, te)
			}
		} else if !errors.As(err, &fe) {
			t.Errorf("status %d: want FatalError, got %T", tc.status, err)
		}
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	retryAt := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", retryAt)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow down"}`)
	}))
	defer srv.Close()

	c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	var te *model.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("want TransientError, got %T", err)
	}
	if !te.HasRetry || te.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %s, want positive HTTP-date delay", te.RetryAfter)
	}
}

func TestRateLimitDelayFromErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Rate limit reached. Please try again in 11.986s."}}`)
	}))
	defer srv.Close()

	c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	var te *model.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("want TransientError, got %T", err)
	}
	if !te.HasRetry || te.RetryAfter != 11986*time.Millisecond {
		t.Fatalf("RetryAfter = %s, want 11.986s", te.RetryAfter)
	}
}

// TestNoHeaderInError guards NFR-SEC-01: an error from a failed call must not
// carry the API key.
func TestNoHeaderInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad")
	}))
	defer srv.Close()
	c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("sk-topsecret"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "sk-topsecret") {
		t.Errorf("error leaked the API key: %v", err)
	}
}

func TestOversizedSuccessResponseIsRejectedAsPartialOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", 1<<20+2))
	}))
	defer srv.Close()

	c := openai.New(openai.WithBaseURL(srv.URL), openai.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), []model.Message{{Role: model.RoleUser, Content: "x"}}, model.Params{Model: "m"})
	var fe *model.FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("want FatalError for oversized partial output, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "refusing partial output") {
		t.Fatalf("err = %v", err)
	}
}
