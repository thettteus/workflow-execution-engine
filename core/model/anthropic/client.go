// Package anthropic is a hand-rolled net/http client for the Anthropic Messages
// API, implementing model.Provider (ADR 0006). No vendor SDK. Nothing in this
// package's request/response types is visible to the engine: the only exported
// surface is New (returning a model.Provider). Request headers — including the
// API key — are never logged (NFR-SEC-01).
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/model"
)

const (
	defaultBaseURL         = "https://api.anthropic.com/v1"
	maxResponseBytes int64 = 1 << 20
	// apiVersion pins the Anthropic API surface this client speaks (ADR 0006).
	apiVersion = "2023-06-01"
	// defaultMaxTokens is used when a Worker declares no max_tokens; Anthropic
	// requires the field.
	defaultMaxTokens = 4096
)

// Client is a model.Provider backed by the Anthropic Messages API.
type Client struct {
	baseURL string
	apiKey  string
	version string
	http    *http.Client
	timeout time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (trailing slashes trimmed).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithAPIKey sets the x-api-key value explicitly, overriding ANTHROPIC_API_KEY.
func WithAPIKey(k string) Option { return func(c *Client) { c.apiKey = k } }

// WithHTTPClient supplies a custom *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTimeout sets the HTTP client timeout when a custom client is not supplied.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// New builds a Client reading ANTHROPIC_API_KEY, targeting the public API with a
// 60s timeout. It returns a model.Provider so callers never depend on this
// package's concrete type (REQ-MODEL-01).
func New(opts ...Option) model.Provider {
	c := &Client{
		baseURL: defaultBaseURL,
		apiKey:  os.Getenv("ANTHROPIC_API_KEY"),
		version: apiVersion,
		timeout: 60 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: c.timeout}
	}
	return c
}

// wire types — private; never cross the model.Provider boundary.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements model.Provider. System messages are hoisted into the
// top-level "system" field (Anthropic separates them from the turn list).
func (c *Client) Complete(ctx context.Context, messages []model.Message, params model.Params) (model.Response, error) {
	var system strings.Builder
	turns := make([]message, 0, len(messages))
	for _, m := range messages {
		if m.Role == model.RoleSystem {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
			continue
		}
		turns = append(turns, message{Role: string(m.Role), Content: m.Content})
	}

	body := map[string]any{
		"model":      params.Model,
		"max_tokens": defaultMaxTokens,
		"messages":   turns,
	}
	if system.Len() > 0 {
		body["system"] = system.String()
	}
	for k, v := range params.Extra {
		switch k {
		case "model", "messages", "system":
			continue // owned by us
		default:
			body[k] = v
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return model.Response{}, &model.FatalError{Err: fmt.Errorf("anthropic: encode request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(raw))
	if err != nil {
		return model.Response{}, &model.FatalError{Err: fmt.Errorf("anthropic: build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", c.version)
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return model.Response{}, &model.TransientError{Err: fmt.Errorf("anthropic: request failed: %w", err)}
	}
	defer resp.Body.Close()

	respBody, truncated, readErr := readLimited(resp.Body, maxResponseBytes)
	if readErr != nil {
		return model.Response{}, &model.TransientError{Err: fmt.Errorf("anthropic: read response: %w", readErr)}
	}
	if resp.StatusCode != http.StatusOK {
		return model.Response{}, classifyStatus(resp, respBody, truncated)
	}
	if truncated {
		return model.Response{}, &model.FatalError{Err: fmt.Errorf("anthropic: response exceeded %d bytes; refusing partial output", maxResponseBytes)}
	}

	var parsed messagesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return model.Response{}, &model.FatalError{Err: fmt.Errorf("anthropic: decode response: %w", err)}
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return model.Response{
		Content:      text.String(),
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
	}, nil
}

// classifyStatus maps a non-200 response to a transient or fatal error
// (REQ-MODEL-05), never logging headers (NFR-SEC-01).
func classifyStatus(resp *http.Response, body []byte, truncated bool) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	if truncated {
		snippet += "... [truncated]"
	}
	base := fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, snippet)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		te := &model.TransientError{Err: base}
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			te.RetryAfter, te.HasRetry = d, true
		}
		return te
	}
	return &model.FatalError{Err: base}
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if date, err := http.ParseTime(v); err == nil {
		d := time.Until(date)
		if d < 0 {
			return 0, false
		}
		return d, true
	}
	return 0, false
}

func readLimited(r io.Reader, max int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}
